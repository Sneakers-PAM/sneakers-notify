// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"net/url"

	bredis "github.com/Bugs5382/go-redis"
	goredis "github.com/redis/go-redis/v9"
)

// redisOptions turns REDIS_URL into client options. The address, database,
// password and rediss:// TLS are applied. Anything else a URL can say is
// refused, so a setting is never silently dropped: the helper has no username
// option, and query options aren't carried across. Errors never quote the URL,
// which holds the password.
func redisOptions(raw string) ([]bredis.Option, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("REDIS_URL isn't a valid URL")
	}
	if u.RawQuery != "" {
		return nil, errors.New("REDIS_URL sets a query option, which notify can't honour")
	}
	opt, err := goredis.ParseURL(raw)
	if err != nil {
		return nil, err
	}
	return redisOptionsFrom(opt)
}

func redisOptionsFrom(opt *goredis.Options) ([]bredis.Option, error) {
	if opt.Username != "" {
		return nil, errors.New("REDIS_URL sets a username, which notify can't honour")
	}
	ropts := []bredis.Option{bredis.WithAddr(opt.Addr), bredis.WithDB(opt.DB)}
	if opt.Password != "" {
		ropts = append(ropts, bredis.WithPassword(opt.Password))
	}
	if opt.TLSConfig != nil {
		ropts = append(ropts, bredis.WithTLS(opt.TLSConfig))
	}
	return ropts, nil
}
