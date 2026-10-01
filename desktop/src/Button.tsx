import { forwardRef } from "react";
import {
  Button as ZiqxButton,
  type ButtonProps,
} from "@ziqx/react-comps/button";
import "./Button.css";

/** App theme and layout adapter for the shared ZIQX button component. */
const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { className = "", variant, size, bgColor, fgColor, ...props },
  ref,
) {
  const classes = new Set(className.split(/\s+/));
  const primary = classes.has("primary");
  const danger = classes.has("danger");
  const text = classes.has("text-button");
  const icon = classes.has("icon-button");
  const control =
    variant === undefined && !classes.has("button") && !text && !icon;
  return (
    <ZiqxButton
      {...props}
      ref={ref}
      className={`dockyard-button ${control ? "dockyard-control " : ""}${className}`}
      variant={
        variant ??
        (primary
          ? "primary"
          : danger
            ? "outline"
            : text || icon || control
              ? "ghost"
              : "secondary")
      }
      size={
        size ??
        (icon ? "icon" : classes.has("small") || text ? "compact" : "default")
      }
      bgColor={bgColor ?? (primary ? "#244e39" : "#ffffff")}
      fgColor={
        fgColor ?? (danger ? "#af594f" : primary ? "#f7faed" : "#314a38")
      }
    />
  );
});

export default Button;
