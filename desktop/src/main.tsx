import React from "react";
import { createRoot } from "react-dom/client";
import { Toaster } from "react-hot-toast";
import App from "./App";
import "./style.css";
import "./macos.css";
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
    <Toaster
      position="bottom-right"
      containerStyle={{ bottom: 24, right: 24 }}
      toastOptions={{
        duration: 5000,
        style: {
          background: "#ffffff",
          color: "#303039",
          border: "1px solid #e5e5ea",
          borderRadius: 9,
          padding: "12px 16px",
          fontFamily: "-apple-system, BlinkMacSystemFont, sans-serif",
          fontSize: 13,
          maxWidth: 420,
        },
        success: {
          iconTheme: { primary: "#0271e3", secondary: "#ffffff" },
        },
        error: {
          duration: 8000,
          style: { background: "#6b2424", color: "#ffffff" },
          iconTheme: { primary: "#f6c9c4", secondary: "#6b2424" },
        },
      }}
    />
  </React.StrictMode>,
);
