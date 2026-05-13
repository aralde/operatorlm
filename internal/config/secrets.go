package config

import (
	"errors"
	"strings"

	"github.com/zalando/go-keyring"
)

const keyringService = "operatorlm"

func parseRef(ref string) (service, account string) {
	if idx := strings.Index(ref, ":"); idx >= 0 {
		return ref[:idx], ref[idx+1:]
	}
	return keyringService, ref
}

func GetSecret(ref string) (string, error) {
	if ref == "" {
		return "", errors.New("empty secret ref")
	}
	service, account := parseRef(ref)
	return keyring.Get(service, account)
}

func SetSecret(ref, value string) error {
	service, account := parseRef(ref)
	return keyring.Set(service, account, value)
}

func DeleteSecret(ref string) error {
	service, account := parseRef(ref)
	err := keyring.Delete(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
