import Button from "./Button";
import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from "react";
import { basicSetup } from "codemirror";
import { EditorState, Compartment } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { indentWithTab } from "@codemirror/commands";
import { yaml } from "@codemirror/lang-yaml";
import { json } from "@codemirror/lang-json";
import {
  StreamLanguage,
  indentUnit,
  HighlightStyle,
  syntaxHighlighting,
} from "@codemirror/language";
import { autocompletion } from "@codemirror/autocomplete";
import {
  linter,
  lintGutter,
  openLintPanel,
  type Diagnostic,
} from "@codemirror/lint";
import { tags } from "@lezer/highlight";
import { composeCompletions, formatCompose, lintCompose } from "./composeLint";

const envLanguage = StreamLanguage.define({
  token(stream) {
    if (stream.eatSpace()) return null;
    if (stream.match(/#.*/)) return "comment";
    if (
      stream.sol() &&
      stream.match(/(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*(?=\s*=)/)
    )
      return "variableName";
    if (stream.match("=")) return "operator";
    stream.skipToEnd();
    return "string";
  },
});
const theme = EditorView.theme({
  "&": {
    backgroundColor: "#fafcf8",
    color: "#263d33",
    fontSize: "15px",
    border: "1px solid #d9e3d2",
    borderRadius: "8px",
    overflow: "hidden",
  },
  "&.cm-focused": { outline: "none", borderColor: "#89ab79", boxShadow: "0 0 0 2px rgba(137, 171, 121, 0.35)" },
  ".cm-scroller": {
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
    lineHeight: "1.7",
    overflow: "auto",
  },
  ".cm-content": { padding: "12px 0", caretColor: "#214b39" },
  ".cm-line": { padding: "0 14px" },
  ".cm-gutters": {
    backgroundColor: "#f0f4ec",
    color: "#667961",
    borderRight: "1px solid #dce4d5",
  },
  // The selection layer sits behind the text, so the active line must stay
  // translucent or it hides a selection on that line.
  ".cm-activeLine": { backgroundColor: "rgba(137, 171, 121, 0.10)" },
  ".cm-activeLineGutter": { backgroundColor: "#e4ecdd" },
  ".cm-selectionLayer .cm-selectionBackground": {
    backgroundColor: "rgba(66, 133, 244, 0.18)",
  },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground": {
    backgroundColor: "rgba(66, 133, 244, 0.30)",
  },
  ".cm-selectionMatch": { backgroundColor: "rgba(66, 133, 244, 0.12)" },
  ".cm-tooltip": {
    backgroundColor: "#fff",
    color: "#263d33",
    border: "1px solid #c4d3b9",
    fontSize: "14px",
  },
  ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
    backgroundColor: "#d6e8c6",
    color: "#173d30",
  },
  ".cm-panels": {
    backgroundColor: "#f0f4ec",
    color: "#263d33",
    fontSize: "14px",
  },
  ".cm-panel.cm-search input": {
    width: "auto",
    padding: "4px 8px",
    fontSize: "14px",
  },
});
const colors = syntaxHighlighting(
  HighlightStyle.define([
    {
      tag: [tags.propertyName, tags.attributeName, tags.variableName],
      color: "#215d63",
      fontWeight: "600",
    },
    { tag: [tags.string, tags.special(tags.string)], color: "#826018" },
    { tag: [tags.number, tags.bool, tags.null], color: "#8058a1" },
    { tag: tags.comment, color: "#65766a", fontStyle: "italic" },
    { tag: [tags.punctuation, tags.operator], color: "#536a5b" },
  ]),
);

export type CodeEditorHandle = { format: (toYaml?: boolean) => void };

const CodeEditor = forwardRef<
  CodeEditorHandle,
  {
    value: string;
    onChange: (value: string) => void;
    kind?: "compose" | "env";
    readOnly?: boolean;
    active?: boolean;
    showToolbar?: boolean;
    onProblems?: (problems: Diagnostic[]) => void;
  }
>(function CodeEditor(
  {
    value,
    onChange,
    kind = "compose",
    readOnly = false,
    active = true,
    showToolbar = true,
    onProblems,
  },
  ref,
) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | undefined>(undefined);
  const change = useRef(onChange);
  change.current = onChange;
  const report = useRef(onProblems);
  report.current = onProblems;
  const editable = useRef(new Compartment());
  const language = useRef(new Compartment());
  const [problems, setProblems] = useState<Diagnostic[]>([]);
  const [cursor, setCursor] = useState("Ln 1, Col 1");
  const [formatError, setFormatError] = useState("");
  const isJson = kind === "compose" && value.trimStart().startsWith("{");
  useEffect(() => {
    if (!host.current) return;
    const instance = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          basicSetup,
          theme,
          colors,
          indentUnit.of("  "),
          keymap.of([indentWithTab]),
          EditorView.contentAttributes.of({
            "aria-label":
              kind === "compose"
                ? "Docker Compose code editor"
                : ".env code editor",
            spellcheck: "false",
            autocorrect: "off",
            autocapitalize: "off",
          }),
          editable.current.of(EditorState.readOnly.of(readOnly)),
          language.current.of(
            kind === "env" ? envLanguage : isJson ? json() : yaml(),
          ),
          ...(kind === "compose"
            ? [
                lintGutter(),
                linter(
                  (v) => {
                    const diagnostics = lintCompose(v.state.doc.toString());
                    setProblems(diagnostics);
                    report.current?.(diagnostics);
                    return diagnostics;
                  },
                  { delay: 350 },
                ),
                autocompletion({
                  override: [
                    (context) => {
                      const word = context.matchBefore(/[\w-]*/);
                      if (!word || (!context.explicit && word.from === word.to))
                        return null;
                      const source = context.state.doc.toString();
                      return {
                        from: word.from,
                        options: composeCompletions(source, context.pos).map(
                          (x) => ({
                            ...x,
                            type: "property",
                            apply: source.trimStart().startsWith("{")
                              ? x.label
                              : `${x.label}: `,
                          }),
                        ),
                        validFor: /^[\w-]*$/,
                      };
                    },
                  ],
                }),
              ]
            : []),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) {
              change.current(update.state.doc.toString());
              setFormatError("");
            }
            if (update.docChanged || update.selectionSet) {
              const pos = update.state.selection.main.head;
              const line = update.state.doc.lineAt(pos);
              setCursor(`Ln ${line.number}, Col ${pos - line.from + 1}`);
            }
          }),
        ],
      }),
    });
    view.current = instance;
    return () => {
      view.current = undefined;
      instance.destroy();
    };
  }, [kind]);
  useEffect(() => {
    const v = view.current;
    if (!v) return;
    if (v.state.doc.toString() !== value)
      v.dispatch({
        changes: { from: 0, to: v.state.doc.length, insert: value },
      });
  }, [value]);
  useEffect(() => {
    const v = view.current;
    if (!v) return;
    v.dispatch({
      effects: [
        editable.current.reconfigure(EditorState.readOnly.of(readOnly)),
        language.current.reconfigure(
          kind === "env" ? envLanguage : isJson ? json() : yaml(),
        ),
      ],
    });
  }, [readOnly, kind, isJson]);
  useEffect(() => {
    if (active) view.current?.requestMeasure();
  }, [active]);
  function format(toYaml = false) {
    if (!view.current || readOnly || kind !== "compose") return;
    try {
      const next = formatCompose(view.current.state.doc.toString(), toYaml);
      view.current.dispatch({
        changes: { from: 0, to: view.current.state.doc.length, insert: next },
        userEvent: "input.format",
      });
      view.current.focus();
      setFormatError("");
    } catch (e) {
      setFormatError(String(e).replace(/^Error: /, ""));
    }
  }
  useImperativeHandle(ref, () => ({ format }));
  const errors = problems.filter((p) => p.severity === "error").length;
  return (
    <div className={`source-editor ${kind === "env" ? "env-editor" : ""}`}>
      {showToolbar && (
        <div className="source-editor-tools">
          <span>
            {kind === "env"
              ? ".env"
              : isJson
                ? "Compose · JSON"
                : "Compose · YAML"}
          </span>
          {kind === "compose" && (
            <div>
              <Button
                type="button"
                className="button small"
                disabled={readOnly}
                onClick={() => format()}
              >
                Format
              </Button>
              {isJson && (
                <Button
                  type="button"
                  className="button small"
                  disabled={readOnly}
                  onClick={() => format(true)}
                >
                  Convert to YAML
                </Button>
              )}
            </div>
          )}
        </div>
      )}
      <div ref={host} />
      <div className="source-editor-status">
        <span>{cursor}</span>
        {kind === "compose" && (
          <Button
            type="button"
            onClick={() => {
              if (view.current) openLintPanel(view.current);
            }}
            className={errors ? "lint-errors" : ""}
          >
            {errors} errors · {problems.length - errors} warnings
          </Button>
        )}
        <span>⌘F Find · Tab Indent</span>
      </div>
      {formatError && (
        <p className="alert error" role="alert">
          {formatError}
        </p>
      )}
    </div>
  );
});

export default CodeEditor;
