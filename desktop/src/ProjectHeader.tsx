import Button from "./Button";
import { ArrowUpRight, Globe2, Layers3, Plus, Settings2 } from "lucide-react";
import type { Project } from "./types";
import Accordion from "./Accordion";

export default function ProjectHeader({
  project: p,
  onAddFlavor,
  onDeploy,
}: {
  project: Project;
  onAddFlavor: () => void;
  onDeploy: () => void;
}) {
  const environment = p.environment[0].toUpperCase() + p.environment.slice(1);
  return (
    <header className="project-header">
      <div className="project-header-top">
        <div className="project-header-identity">
          <h1>{p.app_id}</h1>
          <div className="project-header-badges">
            <span
              className={`tag ${p.environment === "production" ? "green" : "neutral"}`}
            >
              {environment}
            </span>
            <span
              className={`project-status ${p.state === "running" ? "running" : ""}`}
            >
              <i aria-hidden="true" />
              {p.state.replaceAll("_", " ")}
            </span>
          </div>
        </div>
        <div className="project-header-actions">
          <Button type="button" className="button" onClick={onAddFlavor}>
            <Plus size={16} /> Add flavor
          </Button>
          <Button type="button" className="button primary" onClick={onDeploy}>
            <ArrowUpRight size={16} />
            {p.mode === "compose" && p.releases.length
              ? "Edit & deploy"
              : "Deploy Compose"}
          </Button>
        </div>
      </div>
    </header>
  );
}

export function ProjectSummary({
  project: p,
  onConfigure,
  onDomains,
}: {
  project: Project;
  onConfigure: () => void;
  onDomains: () => void;
}) {
  const domains = [...new Set([...p.domains, ...(p.external_domains ?? [])])];
  return (
    <div className="project-summary">
      <section
        className="project-summary-item"
        aria-label="Deployment strategy"
      >
        <h2>Deployment</h2>
        <div className="project-strategy-value">
          <Layers3 size={17} aria-hidden="true" />
          <span>
            {p.zerodowntime
              ? "Seamless updates"
              : p.mode === "compose"
                ? "Service updates"
                : "Single instance"}
          </span>
        </div>
        {!p.zerodowntime &&
          p.mode === "compose" &&
          p.environment === "production" && (
            <Button
              type="button"
              className="text-button project-summary-action"
              onClick={onConfigure}
            >
              <Settings2 size={13} /> Manage service updates
            </Button>
          )}
      </section>
      <section className="project-summary-item" aria-label="Project domains">
        <h2>Domains</h2>
        <div className="project-domain-list">
          {domains.slice(0, 2).map((domain) => (
            <Button
              type="button"
              className="project-domain-link"
              key={domain}
              onClick={onDomains}
            >
              <Globe2 size={14} aria-hidden="true" />
              <span>{domain}</span>
            </Button>
          ))}
          {domains.length === 0 && (
            <span className="muted">No domains assigned</span>
          )}
          {domains.length > 2 && (
            <Button
              type="button"
              className="text-button project-summary-action"
              onClick={onDomains}
            >
              +{domains.length - 2} more
            </Button>
          )}
        </div>
      </section>
      <section className="project-summary-item" aria-label="Project location">
        <h2>Server folder</h2>
        <code className="project-folder">
          /docker/{p.adoption?.source_name ?? p.id}
        </code>
        <Accordion
          className="project-technical-details"
          title="Project details"
        >
          <dl>
            <dt>ID</dt>
            <dd>{p.id}</dd>
            <dt>Type</dt>
            <dd>{p.mode === "compose" ? "Docker Compose" : p.template_id}</dd>
          </dl>
        </Accordion>
      </section>
    </div>
  );
}
