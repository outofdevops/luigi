package luigi

import (
	"context"
	"fmt"
	io "io/ioutil"
	"log"
	"net/http"
	"os"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"google.golang.org/api/iterator"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
)

// PubSubMessage is the payload of a Pub/Sub event.
type PubSubMessage struct {
	Data []byte `json:"data"`
}

const ghTokenSecretSuffix = "admin-token"
const regTokenSecretSuffix = "registration-token"

var projectId = os.Getenv("project_id")

// Luigi consumes a Pub/Sub message containing the GitHub Org name
func Luigi(ctx context.Context, m PubSubMessage) error {

	// GitHub org name.
	ghOrgName := string(m.Data)
	if ghOrgName == "" {
		log.Fatalf("The name of the GH Org cannot be empty")
	}

	// GSM Client setup.
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		log.Fatalf("Failed to setup client: %v", err)
	}

	defer client.Close()

	// Fetch GH access token.
	ghCredsSecret := fetchGHAccessToken(client, ctx, ghOrgName)

	// Call GH API for registration token.
	registrationToken := requestRegistrationToken(ghCredsSecret, ghOrgName)

	// Destroy old versions.
	destroyOlderVersions(client, ctx, ghOrgName)

	// Save new version.
	saveRegistrationToken(client, ctx, ghOrgName, registrationToken)
	return nil
}

func fetchGHAccessToken(client *secretmanager.Client, ctx context.Context, ghOrgName string) string {
	// Build the request.
	accessRequest := &secretmanagerpb.AccessSecretVersionRequest{
		Name: fmt.Sprintf("projects/%s/secrets/%s-%s/versions/latest", projectId, ghOrgName, ghTokenSecretSuffix),
	}

	// Call the API.
	result, err := client.AccessSecretVersion(ctx, accessRequest)
	if err != nil {
		log.Fatalf("Failed to access secret version: %v", err)
	}

	// Return.
	log.Printf("Access Token for org %s found", ghOrgName)
	return string(result.Payload.Data)
}

func requestRegistrationToken(token string, ghOrgName string) string {
	// Build the request.
	req, err := http.NewRequest("POST", fmt.Sprintf("https://api.github.com/orgs/%s/actions/runners/registration-token", ghOrgName), nil)
	if err != nil {
		log.Fatalf("Failed to create registration token request: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	// Call the API.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("Failed to request registration token: %v", err)
	}
	defer resp.Body.Close()

	// Return.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Failed to read body %v", err)
	}

	return string(body)
}

func saveRegistrationToken(client *secretmanager.Client, ctx context.Context, ghOrgName, token string) {
	// Build the request.
	req := &secretmanagerpb.AddSecretVersionRequest{
		Parent: fmt.Sprintf("projects/%s/secrets/%s-%s", projectId, ghOrgName, regTokenSecretSuffix),
		Payload: &secretmanagerpb.SecretPayload{
			Data: []byte(token),
		},
	}

	// Call the API.
	result, err := client.AddSecretVersion(ctx, req)
	if err != nil {
		log.Fatalf("Failed to add secret version: %v", err)
	}
	log.Printf("Added secret version: %s\n", result.Name)
}

func destroyOlderVersions(client *secretmanager.Client, ctx context.Context, ghOrgName string) {
	// Build the request.
	req := &secretmanagerpb.ListSecretVersionsRequest{
		Parent: fmt.Sprintf("projects/%s/secrets/%s-%s", projectId, ghOrgName, regTokenSecretSuffix),
	}

	// Call the API.
	it := client.ListSecretVersions(ctx, req)
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}

		if err != nil {
			log.Fatalf("Failed to list secret versions: %v", err)
		}

		log.Printf("Found secret version %s with state %s\n", resp.Name, resp.State)

		if resp.State != secretmanagerpb.SecretVersion_DESTROYED {
			// Destroy older versions.
			req := &secretmanagerpb.DestroySecretVersionRequest{
				Name: resp.Name,
			}
			log.Printf("Destroying secret version %s", resp.Name)

			if _, err := client.DestroySecretVersion(ctx, req); err != nil {
				log.Fatalf("Failed to destroy secret version: %v", err)
			}
		}

	}
}
