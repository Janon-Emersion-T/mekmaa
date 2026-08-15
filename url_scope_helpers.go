package main

import "net/url"

func mutateURLQuery(raw string, mutate func(url.Values)) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	mutate(query)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func withQueryValue(raw string, key string, value string) string {
	return mutateURLQuery(raw, func(query url.Values) {
		if value == "" {
			query.Del(key)
			return
		}
		query.Set(key, value)
	})
}

func withDivisionQuery(raw string, division string) string {
	return withQueryValue(raw, "division", division)
}

func withMonthQuery(raw string, month string) string {
	return withQueryValue(raw, "month", month)
}
