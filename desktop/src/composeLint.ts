import {
  parseDocument,
  isMap,
  isSeq,
  isScalar,
  isNode,
  visit,
  type CollectionTag,
  type ScalarTag,
} from "yaml";
import schema from "./schemas/compose-spec.json" with { type: "json" };
import validate from "./generated/composeValidator.js";

export type Problem = {
  from: number;
  to: number;
  severity: "error" | "warning";
  message: string;
};
const tags: (ScalarTag | CollectionTag)[] = ["!reset", "!override"].flatMap(
  (tag) => [
    { tag, resolve: (value: string) => value },
    { tag, collection: "map" as const, resolve: (value: unknown) => value },
    { tag, collection: "seq" as const, resolve: (value: unknown) => value },
  ],
);
function document(source: string) {
  return parseDocument(source, {
    prettyErrors: false,
    merge: true,
    customTags: tags,
  });
}
function position(node: unknown, length: number): { from: number; to: number } {
  const range = isNode(node) ? node.range : undefined;
  const from = Math.max(0, Math.min(range?.[0] ?? 0, length));
  return { from, to: Math.max(from, Math.min(range?.[1] ?? from + 1, length)) };
}
export function lintCompose(source: string): Problem[] {
  if (new TextEncoder().encode(source).length > 65536)
    return [
      {
        from: 0,
        to: Math.min(source.length, 1),
        severity: "error",
        message: "Compose files must be 64 KiB or smaller.",
      },
    ];
  if (!source.trim()) return [];
  const doc = document(source);
  const problems: Problem[] = doc.errors.map((e) => ({
    from: e.pos[0],
    to: Math.min(source.length, e.pos[1]),
    severity: "error",
    message: e.message,
  }));
  if (problems.length) return problems.slice(0, 100);
  // Docker-specific merge tags are resolved on the server, not by JSON Schema.
  let hasMergeTag = false;
  visit(doc, (_, node) => {
    if (isNode(node) && (node.tag === "!reset" || node.tag === "!override"))
      hasMergeTag = true;
  });
  if (hasMergeTag)
    return [
      {
        ...position(doc.contents, source.length),
        severity: "warning",
        message: "Compose merge tags will be validated by Docker on the VPS.",
      },
    ];
  let data: unknown;
  try {
    data = doc.toJS({ maxAliasCount: 100 });
  } catch {
    return [
      {
        from: 0,
        to: Math.min(source.length, 1),
        severity: "error",
        message: "An alias is missing or expands too many times.",
      },
    ];
  }
  if (!validate(data)) {
    const seen = new Set<string>();
    for (const error of validate.errors ?? []) {
      if (["anyOf", "oneOf"].includes(error.keyword)) continue;
      const path = error.instancePath
        .split("/")
        .slice(1)
        .map((p) => p.replace(/~1/g, "/").replace(/~0/g, "~"));
      let node: unknown = doc.getIn(path, true);
      let detail = error.message ?? "Check this value.";
      if (
        error.keyword === "additionalProperties" ||
        error.keyword === "unevaluatedProperties"
      ) {
        const field = String(
          error.params.additionalProperty ?? error.params.unevaluatedProperty,
        );
        detail = `Unknown Compose property “${field}”.`;
        if (isMap(node))
          node =
            node.items.find((p) => isScalar(p.key) && p.key.value === field)
              ?.key ?? node;
      }
      const at = position(node, source.length);
      const key = `${at.from}:${detail}`;
      if (!seen.has(key)) {
        seen.add(key);
        problems.push({
          ...at,
          severity: "warning",
          message: `${error.instancePath || "Compose"}: ${detail} Docker checks the resolved configuration before deployment.`,
        });
      }
      if (problems.length >= 50) break;
    }
  }
  return problems;
}

// Formatting preserves comments, aliases, scalar types, and literal ${...} text.
export function formatCompose(source: string, toYaml = false): string {
  const doc = document(source);
  if (doc.errors.length)
    throw new Error("Fix YAML syntax errors before formatting.");
  if (!toYaml && source.trimStart().startsWith("{")) {
    try {
      return JSON.stringify(JSON.parse(source), null, 2) + "\n";
    } catch {
      /* YAML flow map */
    }
  }
  if (toYaml)
    visit(doc, (_, node) => {
      if (isMap(node) || isSeq(node)) node.flow = false;
    });
  return doc.toString({ indent: 2, lineWidth: 0 });
}

type Schema = { [key: string]: any };
function variants(value: Schema, depth = 0): Schema[] {
  if (depth > 12) return [];
  const ref = value.$ref
    ?.split("/")
    .slice(1)
    .reduce((v: any, k: string) => v?.[k], schema);
  return [
    value,
    ...(ref ? variants(ref, depth + 1) : []),
    ...[
      ...(value.allOf ?? []),
      ...(value.oneOf ?? []),
      ...(value.anyOf ?? []),
    ].flatMap((v) => variants(v, depth + 1)),
  ];
}
export function composeCompletions(
  source: string,
  offset: number,
): { label: string; info?: string }[] {
  const doc = document(source);
  let path: string[] = [];
  function walk(node: unknown, current: string[]) {
    if (!isMap(node) && !isSeq(node)) return;
    const r = node.range;
    if (!r || offset < r[0] || offset > r[2]) return;
    path = current;
    if (isMap(node))
      for (const p of node.items) {
        if (isScalar(p.key)) walk(p.value, [...current, String(p.key.value)]);
      }
    else node.items.forEach((value, i) => walk(value, [...current, String(i)]));
  }
  walk(doc.contents, []);
  // A blank/incomplete YAML line may fall beyond a map's parsed range.
  const lineStart = source.lastIndexOf("\n", offset - 1) + 1;
  const currentLine = source.slice(lineStart, offset);
  if (!source.trimStart().startsWith("{")) {
    const indent = currentLine.match(/^ */)?.[0].length ?? 0;
    const parents: { indent: number; key: string }[] = [];
    for (const line of source.slice(0, lineStart).split("\n")) {
      if (!line.trim() || line.trimStart().startsWith("#")) continue;
      const leading = line.match(/^ */)![0].length;
      while (parents.length && parents.at(-1)!.indent >= leading) parents.pop();
      const m = line.match(/^( *)["']?([\w.-]+)["']?:\s*(?:#.*)?$/);
      if (m) parents.push({ indent: leading, key: m[2] });
    }
    while (parents.length && parents.at(-1)!.indent >= indent) parents.pop();
    path = parents.map((x) => x.key);
  }
  let candidates: Schema[] = [schema];
  for (const key of path)
    candidates = candidates
      .flatMap((v) => variants(v))
      .flatMap((v) => {
        if (v.properties?.[key]) return [v.properties[key]];
        if (v.items && /^\d+$/.test(key)) return [v.items];
        return Object.entries(v.patternProperties ?? {})
          .filter(([pattern]) => new RegExp(pattern).test(key))
          .map(([, value]) => value as Schema);
      });
  const options = new Map<string, { label: string; info?: string }>();
  for (const v of candidates.flatMap((v) => variants(v)))
    for (const [label, def] of Object.entries(v.properties ?? {})) {
      options.set(label, { label, info: (def as Schema).description });
    }
  return [...options.values()];
}
