package main

import (
	"context"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/stretchr/testify/assert"
)

func TestAdminApi_EmptyPasswordForbidden(t *testing.T) {
	// Case 1: Config password is empty string
	apiEmpty := &AdminApi{
		App: &App{
			config: &config.SimpleChainConfig{
				Securepassword: "",
			},
		},
	}

	// Login with empty password must fail with errPasswordNotConfigured, not succeed!
	resp, err := apiEmpty.LoginAPI(context.Background(), "")
	assert.ErrorIs(t, err, errPasswordNotConfigured)
	assert.Empty(t, resp)

	// Other admin calls with empty password must also fail
	_, err = apiEmpty.AttestPayloadLoss(context.Background(), "", 1, "00")
	assert.ErrorIs(t, err, errPasswordNotConfigured)

	_, err = apiEmpty.AttestPayloadLossForCommit(context.Background(), "", 1)
	assert.ErrorIs(t, err, errPasswordNotConfigured)

	_, err = apiEmpty.CreateBackup(context.Background(), "")
	assert.ErrorIs(t, err, errPasswordNotConfigured)

	// Case 2: Config password is only whitespace
	apiWhitespace := &AdminApi{
		App: &App{
			config: &config.SimpleChainConfig{
				Securepassword: "   ",
			},
		},
	}
	resp, err = apiWhitespace.LoginAPI(context.Background(), "   ")
	assert.ErrorIs(t, err, errPasswordNotConfigured)
	assert.Empty(t, resp)
}

func TestAdminApi_ValidPasswordAuthentication(t *testing.T) {
	api := &AdminApi{
		App: &App{
			config: &config.SimpleChainConfig{
				Securepassword: "super_secure_admin_pass",
			},
		},
	}

	// Case 1: Wrong password
	resp, err := api.LoginAPI(context.Background(), "wrong_pass")
	assert.ErrorIs(t, err, errInvalidCredentials)
	assert.Empty(t, resp)

	// Case 2: Correct password
	resp, err = api.LoginAPI(context.Background(), "super_secure_admin_pass")
	assert.NoError(t, err)
	assert.Equal(t, "Login successful", resp)
}
