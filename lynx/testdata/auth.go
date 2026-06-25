package auth

type AuthService interface {
	Login() error
}

type Service struct{}

func (s *Service) Login() error {
	return nil
}

func RegisterAuthRoutes() {
}
