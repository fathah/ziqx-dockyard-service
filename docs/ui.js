"use strict";

// Deliberately read-only: no signing keys, credential storage, or operation calls.
const node = (tag, text, className) => { const e = document.createElement(tag); if (text !== undefined) e.textContent = text; if (className) e.className = className; return e; };
const json = value => node("pre", JSON.stringify(value, null, 2));
const resolve = (spec, value) => value && value.$ref ? value.$ref.slice(2).split("/").reduce((v, k) => v[k], spec) : value;
const table = (headers, rows) => { const t = node("table"); const head = node("thead"); const tr = node("tr"); headers.forEach(h => tr.append(node("th", h))); head.append(tr); t.append(head); const body = node("tbody"); rows.forEach(row => { const r = node("tr"); row.forEach(cell => r.append(node("td", cell))); body.append(r); }); t.append(body); return t; };

async function render() {
  const response = await fetch("/openapi.json", {cache: "no-store"});
  if (!response.ok) throw new Error("Cannot load the bundled OpenAPI contract.");
  const spec = await response.json();
  document.getElementById("subtitle").textContent = `${spec.info.title} · ${spec.info.version} · OpenAPI ${spec.openapi}`;
  document.getElementById("protocol").textContent = spec.info.description;
  const container = document.getElementById("operations");
  const cards = [];
  for (const [path, item] of Object.entries(spec.paths)) {
    for (const [method, op] of Object.entries(item)) {
      if (!["get", "post", "put", "patch", "delete"].includes(method)) continue;
      const card = node("details"); const summary = node("summary");
      summary.append(node("span", method.toUpperCase(), `method ${method}`), node("span", path, "path"), node("span", op.summary, "summary-text")); card.append(summary);
      const body = node("div", undefined, "body");
      body.append(node("p", op.description || ""));
      let scopes = op["x-required-scopes"] || [];
      let permissions = scopes.length ? `mTLS + signed request · ${scopes.join(" + ")}` : "mTLS only at the agent listener";
      if (op["x-requires-all-projects"]) permissions += " · all-project access";
      for (const conditional of op["x-conditional-scopes"] || []) permissions += `\n${conditional.when}: ${conditional.scopes.join(" + ")}`;
      body.append(node("p", permissions, "scope"));
      const params = (op.parameters || []).map(p => resolve(spec, p));
      if (params.length) { body.append(node("h3", "Parameters"), table(["Name", "Location", "Required", "Description"], params.map(p => [p.name, p.in, p.required ? "Yes" : "No", [p.description, p.schema.default !== undefined ? `Default: ${p.schema.default}` : ""].filter(Boolean).join(" ")]))); }
      if (op.requestBody) {
        const request = resolve(spec, op.requestBody); const schema = resolve(spec, request.content["application/json"].schema);
        body.append(node("h3", "Request body"), json(schema));
        if (schema.examples) body.append(node("h3", "Example JSON (illustrative values)"), json(schema.examples[0]));
      }
      body.append(node("h3", "Responses"));
      for (const [status, raw] of Object.entries(op.responses)) {
        const r = resolve(spec, raw); const details = node("details"); details.append(node("summary", `${status} · ${r.description}`));
        const content = r.content || {};
        for (const [media, value] of Object.entries(content)) { details.append(node("p", media), json(resolve(spec, value.schema))); }
        body.append(details);
      }
      card.append(body); container.append(card);
      cards.push({card, text: `${method} ${path} ${op.summary} ${op.description} ${permissions}`.toLowerCase()});
    }
  }
  const schemas = document.getElementById("schemas");
  for (const [name, schema] of Object.entries(spec.components.schemas)) { const d = node("details"); d.append(node("summary", name), json(schema)); schemas.append(d); }
  const update = () => { const q = document.getElementById("search").value.trim().toLowerCase(); let visible = 0; cards.forEach(({card, text}) => { card.hidden = !text.includes(q); if (!card.hidden) visible++; }); document.getElementById("count").textContent = `${visible} of ${cards.length} endpoints`; };
  document.getElementById("search").addEventListener("input", update); update();
}

render().catch(error => { const subtitle = document.getElementById("subtitle"); subtitle.textContent = error.message; subtitle.classList.add("error"); });
