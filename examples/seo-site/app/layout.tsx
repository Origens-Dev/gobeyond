import type { ReactNode } from "react";
import { SiteShell } from "../components/site-shell.js";
import "./site.css";

export interface RootLayoutProps {
  children: ReactNode;
}

export default function RootLayout({ children }: RootLayoutProps) {
  return <SiteShell>{children}</SiteShell>;
}
