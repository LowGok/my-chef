package main

import "errors"

func nativeIdentityIngress(value string) (bool, error) {
	switch value {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("MYCHEF_NATIVE_APP_INGRESS must be 0 or 1")
	}
}
