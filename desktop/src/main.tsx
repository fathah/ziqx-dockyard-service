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
          background: "#244e39",
          color: "#f7faed",
          borderRadius: 9,
          padding: "12px 16px",
          fontFamily: "-apple-system, BlinkMacSystemFont, sans-serif",
          fontSize: 13,
          maxWidth: 420,
        },
        success: {
          iconTheme: { primary: "#c9e6a8", secondary: "#244e39" },
        },
      }}
    />
  </React.StrictMode>,
);
