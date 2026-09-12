package accounttools

import (
	"context"
	"errors"

	integration "github.com/domainry/domainry-integration-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
)

// ConversationToolAvailable is a preflight over current account discovery and
// owner authorization. It never probes vendors or substitutes for execution.
func (a *Adapter) ConversationToolAvailable(ctx context.Context, authority sdk.Authority, key string) (bool, error) {
	keys := []string{key}
	if a.Family.AccountsKey != "" && key == a.Family.AccountsKey {
		keys = a.Family.Operations
	} else if a.Family.OperationSHA256(key) == "" {
		return false, nil
	}
	if a.Accounts == nil || a.Reads == nil {
		return false, nil
	}
	listSubject, err := a.subject(ctx, authority, integration.ActionIntegrationConnectionAccountsList)
	if err != nil {
		return false, availabilityError(err)
	}
	readSubject, err := a.subject(ctx, authority, integration.ActionIntegrationConnectionAccountsRead)
	if err != nil {
		return false, availabilityError(err)
	}
	accounts, err := a.Accounts.ListConnectionAccounts(ctx, listSubject)
	if err != nil {
		return false, err
	}
	for _, account := range accounts {
		if !listedAccount(account, listSubject) {
			continue
		}
		for _, opKey := range keys {
			op := integration.ConnectionAccountReadOperation{Operation: opKey, ContractSHA256: a.Family.OperationSHA256(opKey)}
			access, err := a.Reads.AuthorizeConnectionAccountRead(ctx, readSubject, account.Key, op)
			if err == nil && sourceMatches(access.Source, readSubject, account.Key, op) && access.Source.AccountUpdatedAt == account.UpdatedAt && access.Source.ConnectorKey == account.ConnectorKey && access.Source.ProviderKey == account.ProviderKey {
				return true, nil
			}
		}
	}
	return false, nil
}

func availabilityError(err error) error {
	var denied *sdk.Error
	if errors.As(err, &denied) && denied.Class == "forbidden" {
		return nil
	}
	return err
}

var _ sdk.Availability = (*Adapter)(nil)
