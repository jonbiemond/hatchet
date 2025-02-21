package ticker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/hatchet-dev/hatchet/internal/datautils"
	"github.com/hatchet-dev/hatchet/internal/logger"
	"github.com/hatchet-dev/hatchet/internal/repository"
	"github.com/hatchet-dev/hatchet/internal/services/shared/tasktypes"
	"github.com/hatchet-dev/hatchet/internal/taskqueue"
)

type Ticker interface {
	Start(ctx context.Context) error
}

type TickerImpl struct {
	tq   taskqueue.TaskQueue
	l    *zerolog.Logger
	repo repository.Repository
	s    gocron.Scheduler

	crons              sync.Map
	scheduledWorkflows sync.Map
	jobRuns            sync.Map
	stepRuns           sync.Map
	getGroupKeyRuns    sync.Map

	dv datautils.DataDecoderValidator

	tickerId string
}

type timeoutCtx struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type TickerOpt func(*TickerOpts)

type TickerOpts struct {
	tq       taskqueue.TaskQueue
	l        *zerolog.Logger
	repo     repository.Repository
	tickerId string

	dv datautils.DataDecoderValidator
}

func defaultTickerOpts() *TickerOpts {
	logger := logger.NewDefaultLogger("ticker")
	return &TickerOpts{
		l:        &logger,
		tickerId: uuid.New().String(),
		dv:       datautils.NewDataDecoderValidator(),
	}
}

func WithTaskQueue(tq taskqueue.TaskQueue) TickerOpt {
	return func(opts *TickerOpts) {
		opts.tq = tq
	}
}

func WithRepository(r repository.Repository) TickerOpt {
	return func(opts *TickerOpts) {
		opts.repo = r
	}
}

func WithLogger(l *zerolog.Logger) TickerOpt {
	return func(opts *TickerOpts) {
		opts.l = l
	}
}

func New(fs ...TickerOpt) (*TickerImpl, error) {
	opts := defaultTickerOpts()

	for _, f := range fs {
		f(opts)
	}

	if opts.tq == nil {
		return nil, fmt.Errorf("task queue is required. use WithTaskQueue")
	}

	if opts.repo == nil {
		return nil, fmt.Errorf("repository is required. use WithRepository")
	}

	newLogger := opts.l.With().Str("service", "ticker").Logger()
	opts.l = &newLogger

	s, err := gocron.NewScheduler(gocron.WithLocation(time.UTC))

	if err != nil {
		return nil, fmt.Errorf("could not create scheduler: %w", err)
	}

	return &TickerImpl{
		tq:       opts.tq,
		l:        opts.l,
		repo:     opts.repo,
		s:        s,
		dv:       opts.dv,
		tickerId: opts.tickerId,
	}, nil
}

func (t *TickerImpl) Start() (func() error, error) {
	ctx, cancel := context.WithCancel(context.Background())

	t.l.Debug().Msgf("starting ticker %s", t.tickerId)

	// register the ticker
	ticker, err := t.repo.Ticker().CreateNewTicker(&repository.CreateTickerOpts{
		ID: t.tickerId,
	})

	if err != nil {
		cancel()
		return nil, err
	}

	// subscribe to a task queue with the dispatcher id
	cleanupQueue, taskChan, err := t.tq.Subscribe(taskqueue.QueueTypeFromTickerID(ticker.ID))

	if err != nil {
		cancel()
		return nil, err
	}

	_, err = t.s.NewJob(
		gocron.DurationJob(time.Second*5),
		gocron.NewTask(
			t.runGetGroupKeyRunRequeue(ctx),
		),
	)

	if err != nil {
		cancel()
		return nil, fmt.Errorf("could not schedule get group key run requeue: %w", err)
	}

	_, err = t.s.NewJob(
		gocron.DurationJob(time.Second*5),
		gocron.NewTask(
			t.runUpdateHeartbeat(ctx),
		),
	)

	t.s.Start()

	wg := sync.WaitGroup{}

	go func() {
		for task := range taskChan {
			wg.Add(1)
			go func(task *taskqueue.Task) {
				defer wg.Done()

				err := t.handleTask(ctx, task)
				if err != nil {
					t.l.Error().Err(err).Msgf("could not handle ticker task %s", task.ID)
				}
			}(task)
		}
	}()

	cleanup := func() error {
		t.l.Debug().Msg("removing ticker")

		cancel()

		if err := cleanupQueue(); err != nil {
			return fmt.Errorf("could not cleanup queue: %w", err)
		}

		wg.Wait()

		// delete the ticker
		err = t.repo.Ticker().Delete(t.tickerId)

		if err != nil {
			t.l.Err(err).Msg("could not delete ticker")
			return err
		}

		// add the task after the ticker is deleted
		err = t.tq.AddTask(
			ctx,
			taskqueue.JOB_PROCESSING_QUEUE,
			tickerRemoved(t.tickerId),
		)

		if err != nil {
			t.l.Err(err).Msg("could not add ticker removed task")
			return err
		}

		if err := t.s.Shutdown(); err != nil {
			return fmt.Errorf("could not shutdown scheduler: %w", err)
		}

		return nil
	}

	return cleanup, nil
}

func (t *TickerImpl) handleTask(ctx context.Context, task *taskqueue.Task) error {
	switch task.ID {
	case "schedule-step-run-timeout":
		return t.handleScheduleStepRunTimeout(ctx, task)
	case "cancel-step-run-timeout":
		return t.handleCancelStepRunTimeout(ctx, task)
	case "schedule-get-group-key-run-timeout":
		return t.handleScheduleGetGroupKeyRunTimeout(ctx, task)
	case "cancel-get-group-key-run-timeout":
		return t.handleCancelGetGroupKeyRunTimeout(ctx, task)
	case "schedule-job-run-timeout":
		return t.handleScheduleJobRunTimeout(ctx, task)
	case "cancel-job-run-timeout":
		return t.handleCancelJobRunTimeout(ctx, task)
	// case "schedule-step-requeue":
	// 	return t.handleScheduleStepRunRequeue(ctx, task)
	// case "cancel-step-requeue":
	// 	return t.handleCancelStepRunRequeue(ctx, task)
	case "schedule-cron":
		return t.handleScheduleCron(ctx, task)
	case "cancel-cron":
		return t.handleCancelCron(ctx, task)
	case "schedule-workflow":
		return t.handleScheduleWorkflow(ctx, task)
	case "cancel-workflow":
		return t.handleCancelWorkflow(ctx, task)
	}

	return fmt.Errorf("unknown task: %s", task.ID)
}

func (t *TickerImpl) runGetGroupKeyRunRequeue(ctx context.Context) func() {
	return func() {
		t.l.Debug().Msgf("ticker: checking get group key run requeue")

		// list all tenants
		tenants, err := t.repo.Tenant().ListTenants()

		if err != nil {
			t.l.Err(err).Msg("could not list tenants")
			return
		}

		for i := range tenants {
			t.l.Debug().Msgf("adding get group key run requeue task for tenant %s", tenants[i].ID)

			err := t.tq.AddTask(
				ctx,
				taskqueue.WORKFLOW_PROCESSING_QUEUE,
				tasktypes.TenantToGroupKeyActionRequeueTask(tenants[i]),
			)

			if err != nil {
				t.l.Err(err).Msg("could not add get group key run requeue task")
			}
		}
	}
}

func (t *TickerImpl) runUpdateHeartbeat(ctx context.Context) func() {
	return func() {
		t.l.Debug().Msgf("ticker: updating heartbeat")

		now := time.Now().UTC()

		// update the heartbeat
		_, err := t.repo.Ticker().UpdateTicker(t.tickerId, &repository.UpdateTickerOpts{
			LastHeartbeatAt: &now,
		})

		if err != nil {
			t.l.Err(err).Msg("could not update heartbeat")
		}
	}
}

func tickerRemoved(tickerId string) *taskqueue.Task {
	payload, _ := datautils.ToJSONMap(tasktypes.RemoveTickerTaskPayload{
		TickerId: tickerId,
	})

	metadata, _ := datautils.ToJSONMap(tasktypes.RemoveTickerTaskMetadata{})

	return &taskqueue.Task{
		ID:       "ticker-removed",
		Payload:  payload,
		Metadata: metadata,
	}
}
