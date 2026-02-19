package main

type Store struct{}

func NewStore() *Store {
	return &Store{}
}

func (s *Store) Get(key string) string {
	return ""
}

func (s *Store) Set(key, value string) {}

func Delete(key string) {}
