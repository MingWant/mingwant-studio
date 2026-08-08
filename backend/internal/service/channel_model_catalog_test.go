package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestChannelModelNamesFromPayloadNormalizesAndSorts(t *testing.T) {
	models, err := channelModelNamesFromPayload(channelModelsPayload{Data: []channelModelItem{
		{ID: " models/zeta "},
		{Name: "alpha"},
		{ID: "models/alpha"},
		{ID: "models/"},
	}}, "openai")
	if err != nil {
		t.Fatalf("channelModelNamesFromPayload() error = %v", err)
	}
	if got := strings.Join(models, ","); got != "alpha,zeta" {
		t.Fatalf("models = %q", got)
	}
}

func TestChannelModelNamesFromPayloadRejectsOversizedCatalog(t *testing.T) {
	rawItems := make([]channelModelItem, maxChannelModelCatalogEntries+1)
	if _, err := channelModelNamesFromPayload(channelModelsPayload{Data: rawItems}, "openai"); !isCatalogBadGateway(err) {
		t.Fatalf("raw catalog error = %v", err)
	}

	uniqueItems := make([]channelModelItem, maxChannelModelsPerChannel+1)
	for index := range uniqueItems {
		uniqueItems[index].ID = fmt.Sprintf("model-%d", index)
	}
	if _, err := channelModelNamesFromPayload(channelModelsPayload{Data: uniqueItems}, "openai"); !isCatalogBadGateway(err) {
		t.Fatalf("unique catalog error = %v", err)
	}
}

func TestChannelModelNamesFromPayloadRejectsInvalidIdentifiers(t *testing.T) {
	for _, modelKey := range []string{strings.Repeat("m", maxChannelModelKeyRunes+1), "model\nname"} {
		if _, err := channelModelNamesFromPayload(channelModelsPayload{Data: []channelModelItem{{ID: modelKey}}}, "openai"); !isCatalogBadGateway(err) {
			t.Fatalf("model key %q error = %v", modelKey, err)
		}
	}
}

func TestValidatedChannelModelNamesEnforcesUniqueLimit(t *testing.T) {
	values := make([]string, maxChannelModelsPerChannel+1)
	for index := range values {
		values[index] = fmt.Sprintf("model-%d", index)
	}
	if _, err := validatedChannelModelNames(values); err == nil {
		t.Fatal("validatedChannelModelNames() should reject too many unique models")
	}
}

func isCatalogBadGateway(err error) bool {
	var authErr *AuthError
	return errors.As(err, &authErr) && authErr.Status == http.StatusBadGateway
}
