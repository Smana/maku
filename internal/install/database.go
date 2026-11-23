// SPDX-License-Identifier: Apache-2.0

package install

const (
	cnpgClusterName = "rooms-cnpg"
	cnpgCASecret    = "rooms-cnpg-ca" //nolint:gosec // a Secret's name, not a credential
)

// cnpgFiles renders a CloudNativePG Cluster and Database manifest for Maku's three fixed T12 roles
// (v5 DB5, docs/database.md#managed-services-and-operators). None of the three roles holds
// SUPERUSER, CREATEROLE, CREATEDB or BYPASSRLS.
func cnpgFiles(in Inputs) ([]byte, error) {
	ns := in.ns()
	role := func(name, secretName string) map[string]any {
		return map[string]any{
			"name":           name,
			"ensure":         "present",
			"login":          true,
			"superuser":      false,
			"createrole":     false,
			"createdb":       false,
			"bypassrls":      false,
			"passwordSecret": map[string]any{"name": secretName},
		}
	}
	cluster := map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": cnpgClusterName, "namespace": ns},
		"spec": map[string]any{
			"instances": 2,
			"imageName": "ghcr.io/cloudnative-pg/postgresql:17.2",
			"storage":   map[string]any{"size": "20Gi"},
			"postgresql": map[string]any{
				"parameters": map[string]any{ //nolint:gosec // PostgreSQL authentication setting, not a credential
					"password_encryption": "scram-sha-256",
				},
			},
			"managed": map[string]any{
				"roles": []any{
					role("rooms_owner", "rooms-cnpg-owner-password"),
					role("rooms_broker", "rooms-cnpg-broker-password"),
					role("rooms_retention", "rooms-cnpg-retention-password"),
				},
			},
		},
	}
	database := map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Database",
		"metadata":   map[string]any{"name": "rooms", "namespace": ns},
		"spec": map[string]any{
			"name":    "rooms",
			"owner":   "rooms_owner",
			"cluster": map[string]any{"name": cnpgClusterName},
		},
	}
	b, err := docs(cluster, database)
	if err != nil {
		return nil, err
	}
	head := "# Written by maku install --print (database.mode: cnpg): CloudNativePG Cluster and Database.\n" +
		"# Create the three role password Secrets (type kubernetes.io/basic-auth, keys username and password)\n" +
		"# before applying, or build the DSN Secrets from them with ExternalSecrets (docs/database.md).\n" +
		"# CNPG writes the cluster CA into Secret " + cnpgCASecret + " (key ca.crt) automatically.\n"
	return append([]byte(head), b...), nil
}
