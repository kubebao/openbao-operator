package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestLiteralPolicyExpressionsPreserveValues(t *testing.T) {
	const source = `spec:
  variables:
    - name: folded
      expression: >-
        object != null &&
        has(object.metadata)
    - name: nested
      expression: >- # nested CEL expression
        (
          object != null &&
          has(object.metadata)
        )

    - name: paragraphs
      expression: >-
        object != null &&

        has(object.metadata)
    - name: helm
      expression: >-
        '{{ include "openbao-operator.fullname" . }}'
    - name: quoted
      expression: "true"
  validations:
    - expression: >- # validation expression
        variables.folded ||
        variables.nested
      message: >-
        Preserve this folded message.
`
	got, err := literalPolicyExpressions(source)
	require.NoError(t, err)
	var before, after map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(source), &before))
	require.NoError(t, yaml.Unmarshal([]byte(got), &after))
	require.Equal(t, before, after)
	require.NotContains(t, got, "expression: >-")
	require.Contains(t, got, "message: >-")
	require.Contains(t, got, "expression: |- # nested CEL expression")
	require.Contains(t, got, "expression: |- # validation expression")
	require.Contains(t, got, "expression: \"true\"")
	require.Contains(t, got, `{{ include "openbao-operator.fullname" . }}`)
}
