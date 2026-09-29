package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvironmentPromptAndStableRetry(t *testing.T) {
	for answer, want := range map[string]string{"1\n": "development", "2\n": "staging", "3\n": "production"} {
		var output bytes.Buffer
		body := []byte(`{"compose_yaml":"services: {}","variables":{"TOKEN":"$literal"}}`)
		got, err := prepareDeploymentBody("POST", "/v1/projects/demo/deploy", body, "", strings.NewReader(answer), &output)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		json.Unmarshal(got, &object)
		if object["environment"] != want || object["variables"].(map[string]any)["TOKEN"] != "$literal" || !strings.Contains(output.String(), "Which environment") {
			t.Fatal("prompt lost choice or body values")
		}
		retry, err := prepareDeploymentBody("POST", "/v1/projects/demo/deploy", body, want, strings.NewReader(""), &output)
		if err != nil || !bytes.Equal(got, retry) {
			t.Fatal("retry changed signed bytes", err)
		}
	}
}

func TestEnvironmentSelectionRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	for _, tc := range []struct{ body, flag, answer string }{
		{`{}`, "", ""}, {`{}`, "", "4\n"}, {`{}`, "dev", ""},
		{`{"environment":"production"}`, "development", ""},
		{`{"environment":null}`, "", ""},
		{`{"environment":"production","environment":"staging"}`, "", ""},
	} {
		if _, err := prepareDeploymentBody("POST", "/v1/projects", []byte(tc.body), tc.flag, strings.NewReader(tc.answer), &bytes.Buffer{}); err == nil {
			t.Fatal("accepted ambiguous environment", tc)
		}
	}
	// Existing complete bodies keep their exact bytes and do not prompt on retry.
	body := []byte("{ \"environment\" : \"staging\", \"compose_yaml\" : \"services: {}\" }")
	var output bytes.Buffer
	got, err := prepareDeploymentBody("POST", "/v1/projects/demo/deploy", body, "", strings.NewReader(""), &output)
	if err != nil || !bytes.Equal(got, body) || output.Len() != 0 {
		t.Fatal("changed complete body or prompted", err)
	}
}
