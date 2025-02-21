package events

import (
	"context"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/hatchet-dev/hatchet/internal/datautils"
	"github.com/hatchet-dev/hatchet/internal/logger"
	"github.com/hatchet-dev/hatchet/internal/repository"
	"github.com/hatchet-dev/hatchet/internal/repository/prisma/db"
	"github.com/hatchet-dev/hatchet/internal/services/shared/tasktypes"
	"github.com/hatchet-dev/hatchet/internal/taskqueue"
	"github.com/hatchet-dev/hatchet/internal/telemetry"
)

type EventsController interface {
	Start(ctx context.Context) error
}

type EventsControllerImpl struct {
	tq   taskqueue.TaskQueue
	l    *zerolog.Logger
	repo repository.Repository
	dv   datautils.DataDecoderValidator
}

type EventsControllerOpt func(*EventsControllerOpts)

type EventsControllerOpts struct {
	tq   taskqueue.TaskQueue
	l    *zerolog.Logger
	repo repository.Repository
	dv   datautils.DataDecoderValidator
}

func defaultEventsControllerOpts() *EventsControllerOpts {
	logger := logger.NewDefaultLogger("events-controller")
	return &EventsControllerOpts{
		l:  &logger,
		dv: datautils.NewDataDecoderValidator(),
	}
}

func WithTaskQueue(tq taskqueue.TaskQueue) EventsControllerOpt {
	return func(opts *EventsControllerOpts) {
		opts.tq = tq
	}
}

func WithLogger(l *zerolog.Logger) EventsControllerOpt {
	return func(opts *EventsControllerOpts) {
		opts.l = l
	}
}

func WithRepository(r repository.Repository) EventsControllerOpt {
	return func(opts *EventsControllerOpts) {
		opts.repo = r
	}
}

func WithDataDecoderValidator(dv datautils.DataDecoderValidator) EventsControllerOpt {
	return func(opts *EventsControllerOpts) {
		opts.dv = dv
	}
}

func New(fs ...EventsControllerOpt) (*EventsControllerImpl, error) {
	opts := defaultEventsControllerOpts()

	for _, f := range fs {
		f(opts)
	}

	if opts.tq == nil {
		return nil, fmt.Errorf("task queue is required. use WithTaskQueue")
	}

	if opts.repo == nil {
		return nil, fmt.Errorf("repository is required. use WithRepository")
	}

	newLogger := opts.l.With().Str("service", "events-controller").Logger()
	opts.l = &newLogger

	return &EventsControllerImpl{
		tq:   opts.tq,
		l:    opts.l,
		repo: opts.repo,
		dv:   opts.dv,
	}, nil
}

func (ec *EventsControllerImpl) Start() (func() error, error) {
	ctx, cancel := context.WithCancel(context.Background())

	cleanupQueue, taskChan, err := ec.tq.Subscribe(taskqueue.EVENT_PROCESSING_QUEUE)

	if err != nil {
		cancel()
		return nil, fmt.Errorf("could not subscribe to event processing queue: %w", err)
	}

	wg := sync.WaitGroup{}

	go func() {
		for task := range taskChan {
			wg.Add(1)
			go func(task *taskqueue.Task) {
				defer wg.Done()

				err := ec.handleTask(ctx, task)
				if err != nil {
					ec.l.Error().Err(err).Msgf("could not handle event task %s", task.ID)
				}
			}(task)
		}
	}()

	cleanup := func() error {
		cancel()
		if err := cleanupQueue(); err != nil {
			return fmt.Errorf("could not cleanup event processing queue: %w", err)
		}
		return nil
	}

	return cleanup, nil
}

func (ec *EventsControllerImpl) handleTask(ctx context.Context, task *taskqueue.Task) error {
	payload := tasktypes.EventTaskPayload{}
	metadata := tasktypes.EventTaskMetadata{}

	err := ec.dv.DecodeAndValidate(task.Payload, &payload)

	if err != nil {
		return fmt.Errorf("could not decode task payload: %w", err)
	}

	err = ec.dv.DecodeAndValidate(task.Metadata, &metadata)

	if err != nil {
		return fmt.Errorf("could not decode task metadata: %w", err)
	}

	// lookup the event id in the database
	event, err := ec.repo.Event().GetEventById(payload.EventId)

	if err != nil {
		return fmt.Errorf("could not lookup event: %w", err)
	}

	return ec.processEvent(ctx, event)
}

func (ec *EventsControllerImpl) processEvent(ctx context.Context, event *db.EventModel) error {
	ctx, span := telemetry.NewSpan(ctx, "process-event")
	defer span.End()

	tenantId := event.TenantID

	// query for matching workflows in the system
	workflows, err := ec.repo.Workflow().ListWorkflowsForEvent(ctx, tenantId, event.Key)

	if err != nil {
		return fmt.Errorf("could not query workflows for event: %w", err)
	}

	// create a new workflow run in the database
	var g = new(errgroup.Group)

	for _, workflow := range workflows {
		workflowCp := workflow

		g.Go(func() error {
			// create a new workflow run in the database
			createOpts, err := repository.GetCreateWorkflowRunOptsFromEvent(event, &workflowCp)

			if err != nil {
				return fmt.Errorf("could not get create workflow run opts: %w", err)
			}

			workflowRun, err := ec.repo.WorkflowRun().CreateNewWorkflowRun(ctx, tenantId, createOpts)

			if err != nil {
				return fmt.Errorf("could not create workflow run: %w", err)
			}

			// send to workflow processing queue
			return ec.tq.AddTask(
				context.Background(),
				taskqueue.WORKFLOW_PROCESSING_QUEUE,
				tasktypes.WorkflowRunQueuedToTask(workflowRun),
			)
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	return nil
}
