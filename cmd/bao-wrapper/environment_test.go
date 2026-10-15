package main

import (
	"net/url"
	"reflect"
	"testing"
)

func TestChildEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		podIP   string
		apiAddr string
		want    string
	}{
		{"IPv4", "10.0.0.12", "https://10.0.0.12:8200", "https://10.0.0.12:8200"},
		{"IPv6", "2001:db8::12", "https://2001:db8::12:8200", "https://[2001:db8::12]:8200"},
		{"IPv4-mapped IPv6", "::ffff:10.0.0.12", "https://::ffff:10.0.0.12:8200", "https://[::ffff:10.0.0.12]:8200"},
		{"bracketed override", "2001:db8::12", "https://[2001:db8::12]:8200", "https://[2001:db8::12]:8200"},
		{"DNS workaround", "2001:db8::12", "https://bao-0.bao.ns.svc:8200", "https://bao-0.bao.ns.svc:8200"},
		{"other IP override", "2001:db8::12", "https://192.0.2.1:8200", "https://192.0.2.1:8200"},
		{"other port override", "10.0.0.12", "https://10.0.0.12:443", "https://10.0.0.12:443"},
		{"missing Pod IP", "", "https://bao.example.com", "https://bao.example.com"},
		{"invalid Pod IP", "not-an-ip", "https://not-an-ip:8200", "https://not-an-ip:8200"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := []string{"UNCHANGED=a=b", "BAO_API_ADDR=" + tt.apiAddr, "POD_IP=" + tt.podIP}
			before := append([]string(nil), env...)
			got := childEnvironment(env)
			want := []string{"UNCHANGED=a=b", "BAO_API_ADDR=" + tt.want, "POD_IP=" + tt.podIP}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("childEnvironment() = %v, want %v", got, want)
			}
			if !reflect.DeepEqual(env, before) {
				t.Fatal("childEnvironment() changed the parent environment")
			}
			if _, err := url.Parse(tt.want); err != nil {
				t.Fatalf("advertised URL is invalid: %v", err)
			}
		})
	}

	t.Run("no advertisement", func(t *testing.T) {
		env := []string{"POD_IP=2001:db8::12"}
		if got := childEnvironment(env); !reflect.DeepEqual(got, env) {
			t.Fatalf("childEnvironment() = %v, want %v", got, env)
		}
	})
}
