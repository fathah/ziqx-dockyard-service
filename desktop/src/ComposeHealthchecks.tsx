import { useMemo, useState } from "react";
import { HeartPulse } from "lucide-react";
import Accordion from "./Accordion";
import Button from "./Button";
import Select from "./Select";
import {
  configureHealthcheck,
  healthcheckServices,
  validHealthcheckInterval,
  type HealthcheckService,
} from "./composeHealthcheck";

export default function ComposeHealthchecks({
  source,
  disabled,
  blueGreen,
  onChange,
}: {
  source: string;
  disabled: boolean;
  blueGreen: boolean;
  onChange: (source: string, healthcheckDisabled: boolean) => void;
}) {
  const detection = useMemo(() => {
    try {
      return { services: healthcheckServices(source), issue: "" };
    } catch (error) {
      return { services: [], issue: String(error).replace(/^Error: /, "") };
    }
  }, [source]);
  const [service, setService] = useState("");
  const selected =
    detection.services.find((item) => item.name === service) ??
    detection.services[0];
  return (
    <Accordion
      title="Health checks"
      icon={HeartPulse}
      className="compose-healthchecks"
    >
      <p className="configuration-note">
        Choose how often Docker checks each service, or turn checks off when an
        external monitor such as UptimeRobot handles health. Your app’s health
        endpoints stay available.
      </p>
      {detection.issue ? (
        <p role="status">{detection.issue}</p>
      ) : selected ? (
        <>
          <Select
            label="Health check service"
            value={selected.name}
            onValueChange={setService}
            disabled={disabled}
            options={detection.services.map((item) => ({
              value: item.name,
              label: `${item.name} · ${item.disabled ? "Off" : item.interval || "Compose / image defaults"}`,
            }))}
          />
          <HealthcheckSettings
            key={JSON.stringify(selected)}
            service={selected}
            source={source}
            disabled={disabled}
            blueGreen={blueGreen}
            onChange={onChange}
          />
        </>
      ) : (
        <p>Add a service to configure its health checks.</p>
      )}
    </Accordion>
  );
}

function HealthcheckSettings({
  service,
  source,
  disabled,
  blueGreen,
  onChange,
}: {
  service: HealthcheckService;
  source: string;
  disabled: boolean;
  blueGreen: boolean;
  onChange: (source: string, healthcheckDisabled: boolean) => void;
}) {
  const [off, setOff] = useState(service.disabled);
  const [interval, setInterval] = useState(service.interval);
  const [waitForStart, setWaitForStart] = useState(false);
  const [error, setError] = useState("");
  const invalid = !off && !!interval && !validHealthcheckInterval(interval);
  const dependencies = off && service.dependents.length > 0;
  return (
    <div className="healthcheck-settings">
      <div className="form-grid">
        <Select
          label="Automatic health checks"
          value={off ? "off" : "enabled"}
          onValueChange={(value) => {
            setOff(value === "off");
            setError("");
          }}
          disabled={disabled}
          options={[
            { value: "enabled", label: "Enabled" },
            { value: "off", label: "Off" },
          ]}
        />
        {!off && (
          <label>
            Check interval
            <input
              value={interval}
              placeholder="1m"
              disabled={disabled}
              aria-invalid={invalid || undefined}
              onChange={(event) => setInterval(event.target.value.trim())}
            />
            <small>
              For example 30s, 1m or 5m. Empty uses Compose / image defaults.
            </small>
          </label>
        )}
      </div>
      {dependencies && (
        <label className="healthcheck-dependency">
          <input
            type="checkbox"
            checked={waitForStart}
            disabled={disabled}
            onChange={(event) => setWaitForStart(event.target.checked)}
          />
          <span>
            Let {service.dependents.join(", ")} wait for container startup
            instead of a healthy result.
          </span>
        </label>
      )}
      <p className="configuration-note">
        {off
          ? blueGreen
            ? "Blue-green deployments require health checks. Turn off blue-green before disabling them."
            : "Off disables Compose and image health checks. Deployments verify the container is running; seamless updates require a healthcheck."
          : "Uses the existing Compose or image healthcheck command. If the image has no probe, add one in Compose. Startup timing stays as configured."}
      </p>
      {error && (
        <p className="alert error" role="alert">
          {error}
        </p>
      )}
      <Button
        type="button"
        className="button small"
        disabled={
          disabled ||
          invalid ||
          (off && blueGreen) ||
          (dependencies && !waitForStart)
        }
        onClick={() => {
          try {
            const next = configureHealthcheck(source, service.name, {
              disabled: off,
              interval,
              waitForStart,
            });
            setError("");
            onChange(next, off);
          } catch (error) {
            setError(String(error).replace(/^Error: /, ""));
          }
        }}
      >
        Apply to Compose
      </Button>
      <p className="configuration-note">
        Save keeps a draft. Deploy applies these settings to the containers.
      </p>
    </div>
  );
}
