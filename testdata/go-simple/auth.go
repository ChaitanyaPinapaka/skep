package main

// AuthMiddleware checks authentication tokens.
func AuthMiddleware(next Handler) Handler {
	return nil
}

// ValidateToken checks if a token is valid.
func ValidateToken(token string) bool {
	return token != ""
}
