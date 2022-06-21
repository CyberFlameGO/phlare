package agent

import (
	"context"
	"sync"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/services"
	commonconfig "github.com/prometheus/common/config"
	"github.com/prometheus/prometheus/discovery"

	"github.com/grafana/fire/pkg/gen/push/v1/pushv1connect"
)

type Agent struct {
	Config *Config
	services.Service
	logger log.Logger

	manager    *discovery.Manager
	jobs       map[string]discovery.Configs
	groups     map[string]*TargetGroup
	pushClient pushv1connect.PusherClient

	mtx sync.Mutex
}

type TargetManager interface {
	Ready() bool
	ActiveTargets() map[string][]Target
}

func New(config *Config, logger log.Logger) (*Agent, error) {
	httpClient, err := commonconfig.NewClientFromConfig(config.ClientConfig.Client, config.ClientConfig.URL.String())
	if err != nil {
		return nil, err
	}
	a := &Agent{
		Config:     config,
		logger:     logger,
		pushClient: pushv1connect.NewPusherClient(httpClient, config.ClientConfig.URL.String()),
	}
	a.Service = services.NewBasicService(nil, a.running, nil)
	jobs := map[string]discovery.Configs{}
	for _, cfg := range config.ScrapeConfigs {
		jobs[cfg.JobName] = cfg.ServiceDiscoveryConfig.Configs()
	}
	a.jobs = jobs
	a.groups = make(map[string]*TargetGroup, len(jobs))
	return a, nil
}

func (a *Agent) running(ctx context.Context) error {
	a.manager = discovery.NewManager(ctx, log.With(a.logger, "component", "discovery"))
	go func() {
		if err := a.manager.Run(); err != nil {
			level.Error(a.logger).Log("msg", "error running discovery manager", "err", err)
		}
	}()
	if err := a.manager.ApplyConfig(a.jobs); err != nil {
		return nil
	}

	for {
		select {
		case targetGroups := <-a.manager.SyncCh():
			a.mtx.Lock()
			for jobName, groups := range targetGroups {
				level.Info(a.logger).Log("msg", "received target groups", "job", jobName)
				if _, ok := a.groups[jobName]; ok {
					a.groups[jobName].sync(groups)
					continue
				}
				newGroup := NewTargetGroup(ctx, jobName, jobConfig(jobName, a.Config), a.pushClient, a.logger)
				a.groups[jobName] = newGroup
				newGroup.sync(groups)

			}
			a.mtx.Unlock()
		case <-ctx.Done():
			return nil
		}
	}
}

func (a *Agent) ActiveTargets() map[string][]*Target {
	result := map[string][]*Target{}

	// todo: (callum) maybe return not a map + sort so the results don't reorder on every load?
	for g, tg := range a.groups {
		tg.mtx.RLock()
		for _, target := range tg.activeTargets {
			result[g] = append(result[g], target)
		}
	}
	return result
}

func (a *Agent) DroppedTargets() []*Target {
	result := []*Target{}

	for _, tg := range a.groups {
		tg.mtx.RLock()
		for _, target := range tg.droppedTargets {
			result = append(result, target)
		}
	}
	return result
}

func jobConfig(jobName string, config *Config) ScrapeConfig {
	for _, cfg := range config.ScrapeConfigs {
		if cfg.JobName == jobName {
			return *cfg
		}
	}
	return ScrapeConfig{}
}
