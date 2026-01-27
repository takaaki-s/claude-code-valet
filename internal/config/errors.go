package config

import "errors"

var (
	// ErrRepositoryExists はリポジトリが既に存在する場合のエラー
	ErrRepositoryExists = errors.New("repository already exists")

	// ErrRepositoryNotFound はリポジトリが見つからない場合のエラー
	ErrRepositoryNotFound = errors.New("repository not found")
)
