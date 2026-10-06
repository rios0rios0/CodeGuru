package entities

import (
	configHelpers "github.com/rios0rios0/gitforge/v4/pkg/config/domain/helpers"
	logger "github.com/sirupsen/logrus"
	"go.uber.org/dig"
)

// RegisterProviders registers all entity providers with the DIG container.
//
// The webhook server (`serve`) needs *Settings at startup to learn its port,
// secret, allowlists, and worker pool dimensions. CLI subcommands like
// `review` continue to load settings independently inside the controller and
// ignore this provider, so any startup error here is downgraded to a warning
// rather than failing DI for the whole binary.
func RegisterProviders(container *dig.Container) error {
	return container.Provide(provideSettings)
}

func provideSettings() *Settings {
	if path, _ := configHelpers.FindConfigFile("code-guru"); path != "" {
		s, err := NewSettings(path)
		if err == nil {
			return s
		}
		logger.Warnf("settings: failed to load %q: %v -- falling back to env", path, err)
	}
	s, err := NewSettingsFromEnv()
	if err != nil {
		// CLI subcommands (review, review-all, discover, ...) re-load settings
		// inside their controllers and ignore this provider, so a startup error
		// here cannot fail DI for the whole binary. Subcommands that *do* rely
		// on this provider (notably `serve`) must validate the returned struct
		// before using it -- see ServeController.Execute, which exits with a
		// fatal error when required server fields are missing.
		logger.Errorf("settings: env-based load failed: %v -- using empty defaults; "+
			"subcommands that depend on global settings will fail to start", err)
		return &Settings{}
	}
	return s
}
