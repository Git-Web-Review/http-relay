package main

import (
	"testing"
)

func TestEnvHostListTreatsStarAsEveryHost(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty allows every host", raw: "", want: nil},
		{name: "a star allows every host", raw: "*", want: nil},
		{name: "a star anywhere in the list wins", raw: "chat.company.tld, *", want: nil},
		{name: "a star with spaces still wins", raw: "  *  ", want: nil},
		{name: "hosts are lowercased and trimmed", raw: " Chat.Company.TLD , hooks.test ", want: []string{"chat.company.tld", "hooks.test"}},
		{name: "a wildcard prefix is dropped", raw: "*.company.tld", want: []string{"company.tld"}},
		{name: "empty entries are ignored", raw: "a.test,,b.test", want: []string{"a.test", "b.test"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("TEST_WEBHOOK_ALLOWED_HOSTS", testCase.raw)
			got := envHostList("TEST_WEBHOOK_ALLOWED_HOSTS")

			if len(got) != len(testCase.want) {
				t.Fatalf("envHostList(%q) = %v, want %v", testCase.raw, got, testCase.want)
			}
			for index, host := range testCase.want {
				if got[index] != host {
					t.Fatalf("envHostList(%q) = %v, want %v", testCase.raw, got, testCase.want)
				}
			}
		})
	}
}

func TestBlockPrivateNetworksDefaultsToOn(t *testing.T) {
	// Empty, not absent: keeps the test hermetic if the variable happens to be
	// set in the environment of whoever runs the tests.
	t.Setenv("WEBHOOK_BLOCK_PRIVATE_NETWORKS", "")

	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() = %v", err)
	}

	if !config.BlockPrivateNetworks {
		t.Error("BlockPrivateNetworks defaults to false; a user-supplied webhook URL could reach the internal network")
	}
}

func TestBlockPrivateNetworksCanBeTurnedOff(t *testing.T) {
	t.Setenv("WEBHOOK_BLOCK_PRIVATE_NETWORKS", "false")

	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() = %v", err)
	}

	if config.BlockPrivateNetworks {
		t.Error("WEBHOOK_BLOCK_PRIVATE_NETWORKS=false was ignored")
	}
}
