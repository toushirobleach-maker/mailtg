package mailparse

import "net/mail"

func mailAddressList(raw string) ([]string, error) {
	list, err := mail.ParseAddressList(raw)
	if err == nil {
		addresses := make([]string, 0, len(list))
		for _, item := range list {
			addresses = append(addresses, item.Address)
		}
		return addresses, nil
	}

	single, singleErr := mail.ParseAddress(raw)
	if singleErr == nil {
		return []string{single.Address}, nil
	}

	return nil, err
}
