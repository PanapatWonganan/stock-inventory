"use client";

import { FormEvent, useEffect, useRef, useState } from "react";

type Catalog = { id: number; code: string; name: string; unit?: string };
type Balance = { sku: string; name: string; warehouse: string; quantity: number; unit: string };
type Movement = { sku: string; warehouse: string; delta: number };
type Entry = { id: number; kind: string; reason: string; createdAt: string; username: string; movements: Movement[] };
type Rows<T> = { items: T[]; more: boolean };
type Tab = "stock-balances" | "inventory-transactions" | "products" | "warehouses";

const labels: Record<Tab, string> = {
  "stock-balances": "ยอดคงเหลือ", "inventory-transactions": "ประวัติรายการ",
  products: "สินค้า", warehouses: "คลังสินค้า",
};
const kinds: Record<string, string> = { receive: "รับเข้า", issue: "เบิกออก", transfer: "โอนคลัง", adjust: "ปรับยอด" };
const messages: Record<string, string> = {
  INVALID_CREDENTIALS: "ชื่อผู้ใช้หรือรหัสผ่านไม่ถูกต้อง",
  UNAUTHENTICATED: "กรุณาเข้าสู่ระบบอีกครั้ง",
  INVALID_INPUT: "กรุณาตรวจสอบข้อมูลที่กรอก",
  DUPLICATE_CODE: "รหัสนี้ถูกใช้แล้ว",
  INSUFFICIENT_STOCK: "ยอดคงเหลือไม่พอสำหรับรายการนี้",
  STALE_BALANCE: "ยอดคงเหลือเปลี่ยนแล้ว กรุณาตรวจสอบยอดล่าสุดและยืนยันอีกครั้ง",
  IDEMPOTENCY_CONFLICT: "ข้อมูลไม่ตรงกับรายการที่ส่งก่อนหน้า",
  QUANTITY_LIMIT: "ยอดคงเหลือเกินจำนวนที่ระบบรองรับ",
};
const number = new Intl.NumberFormat("th-TH");
const date = new Intl.DateTimeFormat("th-TH", { dateStyle: "short", timeStyle: "short" });

class APIError extends Error {
  constructor(public status: number, code: string) {
    super(messages[code] || "เชื่อมต่อระบบไม่ได้หรือบันทึกไม่สำเร็จ กรุณาลองอีกครั้ง");
  }
}
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch("/api/v1/" + path, init);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new APIError(res.status, data.code);
  return data;
}
function write<T>(path: string, body?: object, method = "POST", key?: string) {
  const headers = new Headers({ "Content-Type": "application/json" });
  if (key) headers.set("Idempotency-Key", key);
  return request<T>(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
}
async function catalog(path: string) {
  const items: Catalog[] = [];
  for (let page = 0; ; page++) {
    const data = await request<Rows<Catalog>>(`${path}?page=${page}`);
    items.push(...data.items);
    if (!data.more) return items;
  }
}
const message = (error: unknown) => error instanceof Error ? error.message : "เกิดข้อผิดพลาด";

export default function Inventory() {
  const [user, setUser] = useState<string | null | undefined>();
  const [tab, setTab] = useState<Tab>("stock-balances");
  const [query, setQuery] = useState("");
  const [warehouse, setWarehouse] = useState("");
  const [kindFilter, setKindFilter] = useState("");
  const [page, setPage] = useState(0);
  const [rows, setRows] = useState<Rows<Balance | Entry | Catalog>>({ items: [], more: false });
  const [products, setProducts] = useState<Catalog[]>([]);
  const [warehouses, setWarehouses] = useState<Catalog[]>([]);
  const [revision, refresh] = useState(0);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [form, setForm] = useState<"movement" | "products" | "warehouses" | null>(null);
  const [edit, setEdit] = useState<Catalog | null>(null);
  const [kind, setKind] = useState("receive");
  const [formError, setFormError] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  const attempt = useRef<{ body: string; key: string } | null>(null);

  useEffect(() => {
    request<{ username: string }>("me").then(data => setUser(data.username)).catch(err => {
      if (!(err instanceof APIError && err.status === 401)) setError(message(err));
      setUser(null);
    });
  }, []);

  useEffect(() => {
    if (!user) return;
    let active = true;
    Promise.all([catalog("products"), catalog("warehouses")]).then(([p, w]) => {
      if (active) { setProducts(p); setWarehouses(w); }
    }).catch(err => { if (active) setError(message(err)); });
    return () => { active = false; };
  }, [user, revision]);

  useEffect(() => {
    if (!user) return;
    const controller = new AbortController();
    const params = new URLSearchParams({ q: query, warehouse, kind: kindFilter, page: String(page) });
    setLoading(true);
    setError("");
    request<Rows<Balance | Entry | Catalog>>(`${tab}?${params}`, { signal: controller.signal })
      .then(data => { if (!controller.signal.aborted) setRows(data); })
      .catch(err => {
        if (controller.signal.aborted) return;
        setRows({ items: [], more: false });
        if (err instanceof APIError && err.status === 401) setUser(null);
        setError(message(err));
      }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [user, tab, query, warehouse, kindFilter, page, revision]);

  useEffect(() => {
    if (form) dialog.current?.showModal();
    else dialog.current?.close();
  }, [form]);

  function navigate(next: Tab) {
    setQuery(""); setWarehouse(""); setKindFilter(""); setPage(0); setTab(next);
  }
  function open(next: NonNullable<typeof form>, item: Catalog | null = null) {
    setEdit(item); setKind("receive"); setFormError(""); setNotice(""); attempt.current = null; setForm(next);
  }
  async function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setError(""); setBusy(true);
    try {
      const result = await write<{ username: string }>("auth/login", Object.fromEntries(data));
      setUser(result.username);
    } catch (err) { setError(message(err)); }
    finally { setBusy(false); }
  }
  async function logout() {
    setBusy(true);
    try { await write("auth/logout"); setUser(null); setNotice(""); }
    catch (err) { setError(message(err)); }
    finally { setBusy(false); }
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setBusy(true); setFormError("");
    try {
      if (form === "movement") {
        const body = {
          kind, productId: Number(data.get("productId")), warehouseId: Number(data.get("warehouseId")),
          destinationId: Number(data.get("destinationId")), quantity: Number(data.get("quantity")),
          expectedQuantity: kind === "adjust" ? Number(data.get("expectedQuantity")) : null,
          reason: String(data.get("reason") || ""),
        };
        const serialized = JSON.stringify(body);
        if (attempt.current?.body !== serialized) attempt.current = { body: serialized, key: crypto.randomUUID() };
        const result = await write<{ id: number }>("inventory-transactions", body, "POST", attempt.current.key);
        setNotice(`บันทึก ${kinds[kind]} แล้ว · รายการ #${result.id}`);
      } else if (form) {
        await write(edit ? `${form}/${edit.id}` : form, Object.fromEntries(data), edit ? "PATCH" : "POST");
        setNotice("บันทึกข้อมูลเรียบร้อย");
      }
      setForm(null); refresh(value => value + 1);
    } catch (err) { setFormError(message(err)); }
    finally { setBusy(false); }
  }

  if (user === undefined) return <main className="login"><p role="status">กำลังเชื่อมต่อคลังสินค้า…</p></main>;
  if (!user) return <main className="login">
    <form className="login-card" onSubmit={login}>
      <span className="brand">STOCK<span>ROOM</span></span>
      <h1>เข้าสู่ระบบคลังสินค้า</h1>
      <label>ชื่อผู้ใช้<input name="username" autoComplete="username" required /></label>
      <label>รหัสผ่าน<input name="password" type="password" autoComplete="current-password" required /></label>
      {error && <p role="alert" className="error">{error}</p>}
      <button className="primary" disabled={busy}>{busy ? "กำลังเข้าสู่ระบบ…" : "เข้าสู่ระบบ"}</button>
    </form>
  </main>;

  const stock = tab === "stock-balances";
  const history = tab === "inventory-transactions";
  const title = labels[tab];
  return <div className="shell">
    <aside>
      <span className="brand">STOCK<span>ROOM</span></span>
      <nav aria-label="เมนูหลัก">
        {(Object.keys(labels) as Tab[]).map(item =>
          <button key={item} aria-current={tab === item ? "page" : undefined} onClick={() => navigate(item)}>{labels[item]}</button>
        )}
      </nav>
      <div className="account"><strong>{user}</strong><button disabled={busy} onClick={logout}>ออกจากระบบ</button></div>
    </aside>
    <main className="content">
      <header><h1>{title}</h1><button className="primary" onClick={() => open(stock || history ? "movement" : tab)}>
        {stock || history ? "บันทึกรายการ" : `เพิ่ม${title}`}
      </button></header>
      {notice && <p className="notice" role="status">{notice}</p>}
      {error && <p className="error" role="alert">{error}</p>}
      <section className="panel" aria-label={title}>
        <div className="filters">
          <input aria-label="ค้นหาตามชื่อหรือรหัส" placeholder="ค้นหาตามชื่อหรือรหัส" value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} />
          {(stock || history) && <select aria-label="คลังสินค้า" value={warehouse} onChange={e => { setWarehouse(e.target.value); setPage(0); }}>
            <option value="">ทุกคลัง</option>{warehouses.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>}
          {history && <select aria-label="ประเภทรายการ" value={kindFilter} onChange={e => { setKindFilter(e.target.value); setPage(0); }}>
            <option value="">ทุกประเภท</option>{Object.entries(kinds).map(([key, label]) => <option key={key} value={key}>{label}</option>)}
          </select>}
        </div>
        <div className="table-scroll"><table>
          <thead><tr>{(stock ? ["SKU", "สินค้า", "คลัง", "คงเหลือ", "หน่วย"] : history ? ["รายการ", "ประเภท", "การเปลี่ยนแปลง", "ผู้บันทึก", "วันที่", "เหตุผล"] : ["รหัส", "ชื่อ", ...(tab === "products" ? ["หน่วย"] : []), "จัดการ"]).map(col => <th scope="col" key={col}>{col}</th>)}</tr></thead>
          <tbody>{!loading && rows.items.map((item, index) => {
            if (stock) {
              const row = item as Balance;
              return <tr key={index}><td className="mono">{row.sku}</td><td>{row.name}</td><td>{row.warehouse}</td><td className="mono quantity">{number.format(row.quantity)}</td><td>{row.unit}</td></tr>;
            }
            if (history) {
              const row = item as Entry;
              return <tr key={row.id}><td className="mono">#{row.id}</td><td><span className="tag">{kinds[row.kind]}</span></td><td>{row.movements.map((m, i) => <div key={i}>{m.sku} · {m.warehouse} <b className={m.delta < 0 ? "negative" : "positive"}>{m.delta > 0 ? "+" : ""}{number.format(m.delta)}</b></div>)}</td><td>{row.username}</td><td>{date.format(new Date(row.createdAt))}</td><td>{row.reason || "—"}</td></tr>;
            }
            const row = item as Catalog;
            return <tr key={row.id}><td className="mono">{row.code}</td><td>{row.name}</td>{tab === "products" && <td>{row.unit}</td>}<td><button className="subtle" onClick={() => open(tab as "products" | "warehouses", row)}>แก้ไข</button></td></tr>;
          })}</tbody>
        </table></div>
        {(loading || !rows.items.length) && <p className="empty" role="status">{loading ? "กำลังโหลด…" : "ไม่พบข้อมูล"}</p>}
        {(page > 0 || rows.more) && <footer><button disabled={loading || page === 0} onClick={() => setPage(p => p - 1)}>ก่อนหน้า</button><span>หน้า {page + 1}</span><button disabled={loading || !rows.more} onClick={() => setPage(p => p + 1)}>ถัดไป</button></footer>}
      </section>
    </main>
    <dialog ref={dialog} aria-labelledby="form-title" onCancel={event => { event.preventDefault(); if (!busy) setForm(null); }}>
      {form && <form onSubmit={save}>
        <h2 id="form-title">{form === "movement" ? "บันทึกรายการสต็อก" : `${edit ? "แก้ไข" : "เพิ่ม"}${labels[form]}`}</h2>
        {form === "movement" ? <>
          {(!products.length || !warehouses.length) && <p className="notice">เพิ่มสินค้าและคลังสินค้าก่อนบันทึกรายการ</p>}
          <label>ประเภทรายการ<select value={kind} onChange={e => setKind(e.target.value)}>{Object.entries(kinds).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
          <label>สินค้า<select name="productId" required defaultValue=""><option value="" disabled>เลือกสินค้า</option>{products.map(p => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}</select></label>
          <label>{kind === "transfer" ? "คลังต้นทาง" : "คลังสินค้า"}<select name="warehouseId" required defaultValue=""><option value="" disabled>เลือกคลัง</option>{warehouses.map(w => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
          {kind === "transfer" && <label>คลังปลายทาง<select name="destinationId" required defaultValue=""><option value="" disabled>เลือกคลังปลายทาง</option>{warehouses.map(w => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>}
          {kind === "adjust" && <label>ยอดในระบบก่อนตรวจนับ<input name="expectedQuantity" type="number" min="0" max={Number.MAX_SAFE_INTEGER} required /></label>}
          <label>{kind === "adjust" ? "ยอดที่นับได้" : "จำนวน"}<input key={kind} name="quantity" type="number" min={kind === "adjust" ? 0 : 1} max={Number.MAX_SAFE_INTEGER} required /></label>
          <label>เหตุผล<textarea name="reason" rows={2} maxLength={500} required={kind === "adjust"} /></label>
        </> : <>
          <label>รหัส<input name="code" required defaultValue={edit?.code} maxLength={80} /></label>
          <label>ชื่อ<input name="name" required defaultValue={edit?.name} maxLength={160} /></label>
          {form === "products" && <label>หน่วย<input name="unit" required defaultValue={edit?.unit} maxLength={40} /></label>}
        </>}
        {formError && <p className="error" role="alert">{formError}</p>}
        <footer><button type="button" disabled={busy} onClick={() => setForm(null)}>ยกเลิก</button><button className="primary" disabled={busy || (form === "movement" && (!products.length || !warehouses.length))}>{busy ? "กำลังบันทึก…" : "บันทึก"}</button></footer>
      </form>}
    </dialog>
  </div>;
}
