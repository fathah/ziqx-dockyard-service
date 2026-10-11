import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Globe2, X } from "lucide-react";
import Button from "./Button";
import RouteSetup from "./RouteSetup";
import * as api from "./api";
import type { Project, Service } from "./types";

export default function DomainConnection({
  project,
  service,
  initialService,
  domains,
  preview,
  execute,
  close,
  blocked = false,
}: {
  project: Project;
  service?: Service;
  initialService?: string;
  domains?: string[];
  preview: boolean;
  execute: (mutation: unknown) => Promise<void>;
  close: () => void;
  blocked?: boolean;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [services, setServices] = useState<Service[]>(service ? [service] : []);
  const [loading, setLoading] = useState(!service);
  const [error, setError] = useState(false);
  const [revision, setRevision] = useState(0);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  useEffect(() => {
    if (service || preview) {
      setLoading(false);
      return;
    }
    let alive = true;
    setLoading(true);
    setError(false);
    api
      .read<{ services: Service[] }>({
        kind: "project",
        project: project.id,
        view: "services",
      })
      .then((data) => {
        if (alive) setServices(data.services ?? []);
      })
      .catch(() => {
        if (alive) setError(true);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [project.id, service, preview, revision]);
  return createPortal(
    <dialog
      ref={dialog}
      className="modal domain-connection-modal"
      aria-label="Connect a domain"
      onCancel={(e) => {
        e.preventDefault();
        if (!busy) close();
      }}
    >
      <div className="modal-heading">
        <div>
          <div className="eyebrow">
            {project.app_id} · {project.environment}
          </div>
          <h2>
            <Globe2 size={22} /> Connect {service ? service.name : "a service"}{" "}
            to a domain
          </h2>
        </div>
        <Button
          type="button"
          className="icon-button"
          aria-label="Close domain connection"
          disabled={busy}
          onClick={close}
        >
          <X size={20} />
        </Button>
      </div>
      <div className="modal-body">
        {project.zerodowntime ? (
          <p className="route-error">
            This project uses whole-stack blue/green routing. Switch to service
            updates in Dockyard before connecting multiple services.
          </p>
        ) : (
          <RouteSetup
            project={project}
            services={services}
            loading={loading}
            unavailable={error}
            blocked={blocked}
            preview={preview}
            execute={execute}
            onClose={close}
            refresh={() => setRevision((n) => n + 1)}
            initialService={service?.name ?? initialService}
            initialDomains={domains}
            onBusyChange={setBusy}
          />
        )}
      </div>
    </dialog>,
    document.body,
  );
}
