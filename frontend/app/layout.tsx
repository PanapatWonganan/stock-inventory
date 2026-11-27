import type { Metadata } from "next";
import "./style.css";

export const metadata: Metadata = {
  title: "Stockroom | ระบบคลังสินค้า",
  description: "จัดการและตรวจสอบสต็อกสินค้า",
};
export default function Layout({ children }: Readonly<{ children: React.ReactNode }>) {
  return <html lang="th"><body>{children}</body></html>;
}
