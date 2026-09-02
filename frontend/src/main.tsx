import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./styles.css";
import { applyInterfaceDensity, loadInterfaceDensity } from "./uiPreferences";

const initialInterfaceDensity = loadInterfaceDensity();
applyInterfaceDensity(initialInterfaceDensity);

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App initialInterfaceDensity={initialInterfaceDensity} />
  </React.StrictMode>,
);
