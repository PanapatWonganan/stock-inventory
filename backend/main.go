package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

//go:embed migrations/*.sql
var migrations embed.FS

type app struct {
	db     *sql.DB
	origin string
	secure bool
}
type object = map[string]any

func respond(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Print(err)
	}
}
func failure(w http.ResponseWriter, status int, code string) {
	respond(w, status, object{"code": code})
}
func internal(w http.ResponseWriter, err error) { log.Print(err); failure(w, 500, "INTERNAL_ERROR") }
func dbFailure(w http.ResponseWriter, err error) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23503":
			failure(w, 400, "UNKNOWN_PRODUCT_OR_WAREHOUSE")
			return
		case "23505":
			failure(w, 409, "DUPLICATE_CODE")
			return
		}
	}
	internal(w, err)
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		failure(w, 400, "INVALID_INPUT")
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		failure(w, 400, "INVALID_INPUT")
		return false
	}
	return true
}
func (a app) cookie(w http.ResponseWriter, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "stock_session", Value: value, Path: "/api/v1", MaxAge: age, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}
func (a app) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string
		Password string
	}
	if !decode(w, r, &input) {
		return
	}
	var id int64
	var passwordHash []byte
	err := a.db.QueryRowContext(r.Context(), "SELECT id,password_hash FROM users WHERE username=$1", input.Username).Scan(&id, &passwordHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		internal(w, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) || bcrypt.CompareHashAndPassword(passwordHash, []byte(input.Password)) != nil {
		failure(w, 401, "INVALID_CREDENTIALS")
		return
	}
	token := hex.EncodeToString(randBytes())
	hash := sha256.Sum256([]byte(token))
	_, err = a.db.ExecContext(r.Context(), "INSERT INTO sessions VALUES ($1,$2,now()+interval '8 hours')", hash[:], id)
	if err != nil {
		internal(w, err)
		return
	}
	a.cookie(w, token, 8*3600)
	respond(w, 200, object{"username": input.Username})
}
func randBytes() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
func (a app) protected(fn func(http.ResponseWriter, *http.Request, int64, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("stock_session")
		if err != nil {
			failure(w, 401, "UNAUTHENTICATED")
			return
		}
		var id int64
		var name string
		hash := sha256.Sum256([]byte(cookie.Value))
		err = a.db.QueryRowContext(r.Context(), `SELECT users.id,users.username FROM sessions JOIN users ON users.id=sessions.user_id WHERE token_hash=$1 AND expires_at>now()`, hash[:]).Scan(&id, &name)
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, 401, "UNAUTHENTICATED")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		fn(w, r, id, name)
	}
}
func (a app) logout(w http.ResponseWriter, r *http.Request, _ int64, _ string) {
	cookie, _ := r.Cookie("stock_session")
	hash := sha256.Sum256([]byte(cookie.Value))
	if _, err := a.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", hash[:]); err != nil {
		internal(w, err)
		return
	}
	a.cookie(w, "", -1)
	respond(w, 200, object{})
}
func (a app) list(query string) http.HandlerFunc {
	return a.protected(func(w http.ResponseWriter, r *http.Request, _ int64, _ string) {
		page := 0
		if raw := r.URL.Query().Get("page"); raw != "" {
			var err error
			page, err = strconv.Atoi(raw)
			if err != nil || page < 0 || page > 100000 {
				failure(w, 400, "INVALID_INPUT")
				return
			}
		}
		rows, err := a.db.QueryContext(r.Context(), query, r.URL.Query().Get("q"), r.URL.Query().Get("warehouse"), r.URL.Query().Get("kind"), page*50)
		if err != nil {
			internal(w, err)
			return
		}
		defer rows.Close()
		items := make([]json.RawMessage, 0, 50)
		for rows.Next() {
			var row json.RawMessage
			if err := rows.Scan(&row); err != nil {
				internal(w, err)
				return
			}
			items = append(items, row)
		}
		if err := rows.Err(); err != nil {
			internal(w, err)
			return
		}
		more := len(items) > 50
		if more {
			items = items[:50]
		}
		respond(w, 200, object{"items": items, "more": more})
	})
}
func (a app) catalog(warehouse bool) http.HandlerFunc {
	return a.protected(func(w http.ResponseWriter, r *http.Request, _ int64, _ string) {
		var input struct {
			Code string
			Name string
			Unit string
		}
		if !decode(w, r, &input) {
			return
		}
		input.Code = strings.TrimSpace(input.Code)
		input.Name = strings.TrimSpace(input.Name)
		input.Unit = strings.TrimSpace(input.Unit)
		if input.Code == "" || input.Name == "" || (!warehouse && input.Unit == "") {
			failure(w, 400, "INVALID_INPUT")
			return
		}
		var id int64
		if raw := r.PathValue("id"); raw != "" {
			var err error
			id, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || id < 1 {
				failure(w, 400, "INVALID_INPUT")
				return
			}
		}
		var err error
		switch {
		case warehouse && id == 0:
			err = a.db.QueryRowContext(r.Context(), "INSERT INTO warehouses(code,name) VALUES($1,$2) RETURNING id", input.Code, input.Name).Scan(&id)
		case warehouse:
			err = a.db.QueryRowContext(r.Context(), "UPDATE warehouses SET code=$1,name=$2 WHERE id=$3 RETURNING id", input.Code, input.Name, id).Scan(&id)
		case id == 0:
			err = a.db.QueryRowContext(r.Context(), "INSERT INTO products(sku,name,unit) VALUES($1,$2,$3) RETURNING id", input.Code, input.Name, input.Unit).Scan(&id)
		default:
			err = a.db.QueryRowContext(r.Context(), "UPDATE products SET sku=$1,name=$2,unit=$3 WHERE id=$4 RETURNING id", input.Code, input.Name, input.Unit, id).Scan(&id)
		}
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, 404, "NOT_FOUND")
			return
		}
		if err != nil {
			dbFailure(w, err)
			return
		}
		status := 200
		if r.Method == "POST" {
			status = 201
		}
		respond(w, status, object{"id": id})
	})
}

const productsQuery = `SELECT json_build_object('id',id,'code',sku,'name',name,'unit',unit) FROM products WHERE (strpos(lower(sku||' '||name),lower($1))>0 OR $1='') AND $2::text IS NOT NULL AND $3::text IS NOT NULL ORDER BY sku LIMIT 51 OFFSET $4`
const warehousesQuery = `SELECT json_build_object('id',id,'code',code,'name',name) FROM warehouses WHERE (strpos(lower(code||' '||name),lower($1))>0 OR $1='') AND $2::text IS NOT NULL AND $3::text IS NOT NULL ORDER BY code LIMIT 51 OFFSET $4`
const balancesQuery = `SELECT json_build_object('sku',p.sku,'name',p.name,'warehouse',w.name,'quantity',b.quantity,'unit',p.unit)
FROM stock_balances b JOIN products p ON p.id=b.product_id JOIN warehouses w ON w.id=b.warehouse_id
WHERE (strpos(lower(p.sku||' '||p.name),lower($1))>0 OR $1='') AND ($2='' OR w.id::text=$2) AND $3::text IS NOT NULL ORDER BY p.sku,w.code LIMIT 51 OFFSET $4`
const historyQuery = `SELECT json_build_object('id',t.id,'kind',t.kind,'reason',t.reason,'createdAt',t.created_at,'username',u.username,
'movements',(SELECT json_agg(json_build_object('sku',p.sku,'warehouse',w.name,'delta',m.delta) ORDER BY m.delta,m.warehouse_id)
FROM inventory_movements m JOIN products p ON p.id=m.product_id JOIN warehouses w ON w.id=m.warehouse_id WHERE m.transaction_id=t.id))
FROM inventory_transactions t JOIN users u ON u.id=t.user_id
WHERE ($3='' OR t.kind=$3) AND EXISTS(SELECT 1 FROM inventory_movements m JOIN products p ON p.id=m.product_id
WHERE m.transaction_id=t.id AND ($2='' OR m.warehouse_id::text=$2) AND (strpos(lower(p.sku||' '||p.name),lower($1))>0 OR $1=''))
ORDER BY t.id DESC LIMIT 51 OFFSET $4`

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dsn := os.Getenv("DATABASE_URL")
	origin := os.Getenv("APP_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3000"
	}
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(16)
	schema, err := migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		log.Fatal(err)
	}
	migration, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	if _, err = migration.ExecContext(ctx, "SELECT pg_advisory_xact_lock(18496720)"); err != nil {
		log.Fatal(err)
	}
	if _, err = migration.ExecContext(ctx, string(schema)); err != nil {
		log.Fatal(err)
	}
	if err = migration.Commit(); err != nil {
		log.Fatal(err)
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil {
		log.Fatal(err)
	}
	if count == 0 {
		username, password := os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD")
		if username == "" || len(password) < 12 {
			log.Fatal("Set ADMIN_USERNAME and ADMIN_PASSWORD (at least 12 bytes) to create the first account")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			log.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, "INSERT INTO users(username,password_hash) VALUES($1,$2) ON CONFLICT DO NOTHING", username, hash); err != nil {
			log.Fatal(err)
		}
	}
	a := app{db: db, origin: origin, secure: strings.HasPrefix(origin, "https://")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.PingContext(r.Context()); err != nil {
			failure(w, 503, "DATABASE_UNAVAILABLE")
			return
		}
		respond(w, 200, object{"status": "ok"})
	})
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.HandleFunc("POST /api/v1/auth/logout", a.protected(a.logout))
	mux.HandleFunc("GET /api/v1/me", a.protected(func(w http.ResponseWriter, r *http.Request, _ int64, name string) {
		respond(w, 200, object{"username": name})
	}))
	mux.HandleFunc("GET /api/v1/products", a.list(productsQuery))
	mux.HandleFunc("GET /api/v1/warehouses", a.list(warehousesQuery))
	mux.HandleFunc("POST /api/v1/products", a.catalog(false))
	mux.HandleFunc("PATCH /api/v1/products/{id}", a.catalog(false))
	mux.HandleFunc("POST /api/v1/warehouses", a.catalog(true))
	mux.HandleFunc("PATCH /api/v1/warehouses/{id}", a.catalog(true))
	mux.HandleFunc("GET /api/v1/stock-balances", a.list(balancesQuery))
	mux.HandleFunc("GET /api/v1/inventory-transactions", a.list(historyQuery))
	mux.HandleFunc("POST /api/v1/inventory-transactions", a.protected(a.record))
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	log.Printf("Inventory API listening on %s", addr)
	server := http.Server{Addr: addr, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != a.origin {
			failure(w, 403, "INVALID_ORIGIN")
			return
		}
		mux.ServeHTTP(w, r)
	})}
	log.Fatal(server.ListenAndServe())
}
