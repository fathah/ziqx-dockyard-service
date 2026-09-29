package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

func prepareDeploymentBody(method, target string, body []byte, choice string, input io.Reader, output io.Writer) ([]byte, error) {
	deployment := method == "POST" && (target == "/v1/projects" || strings.HasPrefix(target, "/v1/projects/") && strings.HasSuffix(target, "/deploy"))
	if !deployment {
		if choice != "" {
			return nil, errors.New("environment applies only to project creation or Compose deployment")
		}
		return body, nil
	}
	var object map[string]json.RawMessage
	if secure.Decode(body, &object) != nil || object == nil {
		return nil, errors.New("create/deploy requires a JSON object body")
	}
	if raw, exists := object["environment"]; exists {
		var selected string
		if json.Unmarshal(raw, &selected) != nil || !model.ValidEnvironment(selected) {
			return nil, errors.New("invalid deployment environment in body")
		}
		if choice != "" && choice != selected {
			return nil, errors.New("environment flag conflicts with body")
		}
		return body, nil // Preserve exact bytes for existing retry identities.
	}
	if choice == "" {
		fmt.Fprintln(output, "Which environment should receive this deployment?\n1. development (one instance)\n2. staging (one instance)\n3. production (blue-green eligible)\nEnter 1, 2, 3 or the environment name:")
		line, err := bufio.NewReader(io.LimitReader(input, 256)).ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		choice = strings.TrimSpace(line)
		switch choice {
		case "1":
			choice = model.Development
		case "2":
			choice = model.Staging
		case "3":
			choice = model.Production
		}
	}
	if !model.ValidEnvironment(choice) {
		return nil, errors.New("choose development, staging or production; unattended calls require -environment or an environment in the JSON body")
	}
	object["environment"], _ = json.Marshal(choice)
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 128<<10 {
		return nil, errors.New("body exceeds limit")
	}
	fmt.Fprintf(output, "Selected %s. For retries with this body file, reuse -environment %s and the same request/idempotency IDs.\n", choice, choice)
	return encoded, nil
}
