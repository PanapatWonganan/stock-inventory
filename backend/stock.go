package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

const maxQuantity int64 = 9007199254740991

type movementRequest struct {
	Kind             string
	ProductID        int64
	WarehouseID      int64
	DestinationID    int64
	Quantity         *int64
	ExpectedQuantity *int64
	Reason           string
}

func (input movementRequest) valid() bool {
	if input.ProductID < 1 || input.WarehouseID < 1 || input.Quantity == nil || *input.Quantity < 0 || *input.Quantity > maxQuantity {
		return false
	}
	switch input.Kind {
	case "transfer":
		return *input.Quantity > 0 && input.DestinationID > 0 && input.DestinationID != input.WarehouseID && input.ExpectedQuantity == nil
	case "receive", "issue":
		return *input.Quantity > 0 && input.DestinationID == 0 && input.ExpectedQuantity == nil
	case "adjust":
		return input.ExpectedQuantity != nil && *input.ExpectedQuantity >= 0 && input.DestinationID == 0 && input.Reason != ""
	}
	return false
}

func (a app) record(w http.ResponseWriter, r *http.Request, userID int64, _ string) {
	var input movementRequest
	if !decode(w, r, &input) {
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !input.valid() || key == "" || len(key) > 128 {
		failure(w, 400, "INVALID_INPUT")
		return
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		internal(w, err)
		return
	}
	hash := sha256.Sum256(encoded)
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO inventory_transactions(user_id,request_key,request_hash,kind,reason)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT(user_id,request_key) DO NOTHING RETURNING id`, userID, key, hash[:], input.Kind, input.Reason).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var prior []byte
		err = tx.QueryRowContext(ctx, "SELECT id,request_hash FROM inventory_transactions WHERE user_id=$1 AND request_key=$2", userID, key).Scan(&id, &prior)
		if err != nil {
			internal(w, err)
			return
		}
		if !bytes.Equal(prior, hash[:]) {
			failure(w, 409, "IDEMPOTENCY_CONFLICT")
			return
		}
		respond(w, 200, object{"id": id})
		return
	}
	if err != nil {
		internal(w, err)
		return
	}

	warehouses := []int64{input.WarehouseID}
	if input.Kind == "transfer" {
		warehouses = append(warehouses, input.DestinationID)
	}
	sort.Slice(warehouses, func(i, j int) bool { return warehouses[i] < warehouses[j] })
	for _, warehouse := range warehouses {
		if _, err = tx.ExecContext(ctx, "INSERT INTO stock_balances(product_id,warehouse_id) VALUES($1,$2) ON CONFLICT DO NOTHING", input.ProductID, warehouse); err != nil {
			dbFailure(w, err)
			return
		}
		var current int64
		err = tx.QueryRowContext(ctx, "SELECT quantity FROM stock_balances WHERE product_id=$1 AND warehouse_id=$2 FOR UPDATE", input.ProductID, warehouse).Scan(&current)
		if err != nil {
			internal(w, err)
			return
		}
		delta := *input.Quantity
		switch {
		case input.Kind == "adjust":
			if current != *input.ExpectedQuantity {
				failure(w, 409, "STALE_BALANCE")
				return
			}
			delta = *input.Quantity - current
		case input.Kind == "issue" || (input.Kind == "transfer" && warehouse == input.WarehouseID):
			delta = -delta
		}
		next := current + delta
		if next < 0 {
			failure(w, 409, "INSUFFICIENT_STOCK")
			return
		}
		if next > maxQuantity {
			failure(w, 409, "QUANTITY_LIMIT")
			return
		}
		if _, err = tx.ExecContext(ctx, "UPDATE stock_balances SET quantity=$1 WHERE product_id=$2 AND warehouse_id=$3", next, input.ProductID, warehouse); err != nil {
			internal(w, err)
			return
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO inventory_movements VALUES($1,$2,$3,$4)", id, input.ProductID, warehouse, delta); err != nil {
			internal(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	respond(w, 201, object{"id": id})
}
