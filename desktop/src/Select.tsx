import {
  Select as ZiqxSelect,
  type SelectProps,
} from "@ziqx/react-comps/select";
import { useCallback, useState } from "react";
import "@ziqx/react-comps/select/styles.css";
import "./Select.css";

/** Shared package select with Dockyard's field colors and modal stacking. */
export default function Select({
  containerClassName = "",
  className = "",
  contentProps,
  ...props
}: SelectProps) {
  const [dialogContainer, setDialogContainer] = useState<HTMLElement>();
  const captureTrigger = useCallback((node: HTMLButtonElement | null) => {
    // Native modal dialogs occupy the browser's top layer. Their dropdowns
    // must stay inside the dialog rather than portal to the document body.
    if (node) setDialogContainer(node.closest("dialog") ?? undefined);
  }, []);
  return (
    <ZiqxSelect
      bgColor="#fafcf7"
      fgColor="#354d3b"
      {...props}
      ref={captureTrigger}
      portalContainer={props.portalContainer ?? dialogContainer}
      containerClassName={`dockyard-select-field ${containerClassName}`.trim()}
      className={`dockyard-select ${className}`.trim()}
      contentProps={{
        ...contentProps,
        style: { zIndex: 200, ...contentProps?.style },
      }}
    />
  );
}
