// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Command server runs the obligations service: acknowledgements and views,
// obligations, completion reports, notification preferences, digests and
// delivery by email, in-app and push, over gRPC. It calls core and identity,
// consumes their events and workflow's, and publishes steward-audit's
// AuditEvent through a transactional outbox.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	buildinfo "github.com/Bugs5382/go-buildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
	email "github.com/Bugs5382/go-email"
	log "github.com/Bugs5382/go-log"
	gootel "github.com/Bugs5382/go-otel"
	outbox "github.com/Bugs5382/go-outbox"
	outboxrabbitmq "github.com/Bugs5382/go-outbox/rabbitmq"
	postgres "github.com/Bugs5382/go-postgres"
	pgotel "github.com/Bugs5382/go-postgres/otel"
	"github.com/Bugs5382/go-rabbitmq"
	rmqotel "github.com/Bugs5382/go-rabbitmq/otel"
	redis "github.com/Bugs5382/go-redis"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	corev1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/core/v1"
	identityv1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/identity/v1"
	ackpkg "github.com/Steward-GRC/steward-obligations/internal/ack"
	"github.com/Steward-GRC/steward-obligations/internal/audit"
	"github.com/Steward-GRC/steward-obligations/internal/cache"
	"github.com/Steward-GRC/steward-obligations/internal/config"
	"github.com/Steward-GRC/steward-obligations/internal/consumer"
	"github.com/Steward-GRC/steward-obligations/internal/emailservice"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/notifprefs"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/notifytoken"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
	"github.com/Steward-GRC/steward-obligations/internal/readiness"
	"github.com/Steward-GRC/steward-obligations/internal/reporting"
	"github.com/Steward-GRC/steward-obligations/internal/scheduler"
	"github.com/Steward-GRC/steward-obligations/internal/server"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

const serviceName = "obligations"

// auditOutboxTable holds the audit events waiting for the relay.
const auditOutboxTable = "audit_outbox"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	lg := log.NewLogger(serviceName)
	if err := run(ctx, lg, log.New(serviceName)); err != nil {
		lg.Fatal(err, "obligations service stopped")
	}
}

func run(ctx context.Context, lg log.Logger, logger zerolog.Logger) error {
	bi := buildinfo.Get()
	lg.Info("starting", log.F("version", bi.Version), log.F("commit", bi.Commit), log.F("go_version", bi.GoVersion))
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	otelShutdown, err := gootel.Init(ctx, serviceName, cfg.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("otel: %w", err)
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			lg.Warn("otel shutdown", log.F("error", err.Error()))
		}
	}()

	if err := pgotel.InstrumentMigrate(ctx, serviceName, func() error {
		return postgres.Migrate(cfg.MigrateDSN, cfg.MigrationsDir)
	}); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	db, err := postgres.New(ctx, cfg.DatabaseDSN, pgotel.WithTracing())
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer db.Close()

	mqConn, err := rabbitmq.Connect(ctx, cfg.RabbitURL, append(rmqotel.Instrument(), rabbitmq.WithLogger(rabbitLogger{logger}))...)
	if err != nil {
		return fmt.Errorf("rabbitmq: %w", err)
	}
	defer func() { _ = mqConn.Close() }()

	// Every audit event is written to the outbox, inside the acknowledgement's
	// own transaction where there is one, and the relay ships it to the audit
	// exchange after the commit.
	ob, err := outbox.New(outbox.WithTable(auditOutboxTable), outbox.WithLogger(lg))
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	if err := ob.Migrate(ctx, db); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	relay := ob.NewRelay(db, outboxrabbitmq.New(mqConn, audit.Exchange,
		rabbitmq.WithExchangeDeclare(rabbitmq.ExchangeConfig{Name: audit.Exchange, Kind: "topic", Durable: true})))
	go func() {
		if err := relay.Run(ctx); err != nil {
			lg.Error(err, "audit outbox relay stopped")
		}
	}()
	auditEmitter := audit.New(store.NewOutboxPublisher(db, ob, audit.ContentType))

	ackRowStore := store.NewAcknowledgmentStore(db)
	ackStoreAdapter := store.NewAckStoreAdapter(ackRowStore)
	notifPrefStore := store.NewNotificationPrefStore(db)
	notifCategoryStore := store.NewNotificationCategoryPrefStore(db)
	notifOverrideStore := store.NewNotificationTypeOverrideStore(db)
	notifDigestStore := store.NewNotificationDigestWindowStore(db)
	notifStore := store.NewNotificationStore(db)
	notifOutboxStore := store.NewNotificationOutboxStore(db)
	notifSentStore := store.NewNotificationSentStore(db, mail.DefaultDedupWindow)
	notifPrefReader := notifprefs.NewReader(notifPrefStore, notifCategoryStore, notifOverrideStore)
	notifSettings := notifprefs.NewService(notifPrefStore, notifCategoryStore, notifOverrideStore, notifDigestStore)
	policyViewStore := store.NewPolicyViewStore(db)

	// Service mTLS for outbound calls comes with the service identities; the
	// cluster network is trusted until then.
	dial := func(addr string) (*grpc.ClientConn, error) {
		return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithStatsHandler(gootel.GRPCClientStatsHandler()))
	}
	coreConn, err := dial(cfg.CoreGRPCAddr)
	if err != nil {
		return fmt.Errorf("dial core: %w", err)
	}
	defer func() { _ = coreConn.Close() }()
	identityConn, err := dial(cfg.IdentityAddr)
	if err != nil {
		return fmt.Errorf("dial identity: %w", err)
	}
	defer func() { _ = identityConn.Close() }()

	deps := readiness.Deps{
		Postgres: readiness.PostgresDB(db), Broker: mqConn,
		Core: readiness.GRPCServing(coreConn), Identity: readiness.GRPCServing(identityConn),
		Render: readiness.HTTPReady(&http.Client{Timeout: 2 * time.Second}, cfg.RenderSidecarURL),
	}

	// The caches are optional: without Valkey every read goes to core.
	var kv cache.KV
	if cfg.RedisAddr != "" {
		rc, err := redis.Connect(ctx, redis.WithAddr(cfg.RedisAddr), redis.WithPassword(cfg.RedisPassword),
			redis.WithTimeouts(300*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond))
		if err != nil {
			lg.Warn("redis unreachable: the caches are off", log.F("error", err.Error()))
			bootErr := err
			deps.Cache = func(context.Context) error { return bootErr }
		} else {
			defer func() { _ = rc.Close() }()
			r := cache.New(rc)
			deps.Cache = r.Ping
			kv = r
			lg.Info("caches on", log.F("obligating_ttl_sec", cfg.ObligatingCacheTTLSec))
		}
	}

	policies := corev1.NewPolicyServiceClient(coreConn)
	cachedCore := obligation.NewCachedCore(obligation.NewCoreAdapter(policies), kv, time.Duration(cfg.ObligatingCacheTTLSec)*time.Second)
	resolver := obligation.NewResolver(cachedCore,
		obligation.NewIdentityAdapter(identityv1.NewIdentityReadServiceClient(identityConn)),
		obligation.NewChainAdapter(corev1.NewCategoryServiceClient(coreConn)),
		ackRowStore, policyViewStore)

	identityClient := &identityGRPCAdapter{client: identityv1.NewIdentityReadServiceClient(identityConn)}
	// One resolver backs both the sender's suppress hook and the dispatcher,
	// so quiet hours are evaluated the same way in the user's own time zone.
	prefResolver := notifpolicy.NewResolver(notifPrefReader, notify.QuietHours{Resolver: identityClient})

	senderCfg := mail.SenderConfig{
		SMTPHost:        cfg.SMTPHost,
		SMTPPort:        strconv.Itoa(cfg.SMTPPort),
		SMTPUsername:    cfg.SMTPUsername,
		From:            cfg.SMTPFrom,
		Password:        cfg.SMTPPassword,
		SidecarURL:      cfg.RenderSidecarURL,
		AppEnv:          cfg.AppEnv,
		SMTPStartTLS:    cfg.SMTPStartTLS,
		SMTPTLSInsecure: cfg.SMTPTLSInsecure,
		DevCatchAllTo:   cfg.DevMailCatchAllTo,
		LogoSrc:         cfg.EmailLogoSrc,
		ProductName:     cfg.EmailProductName,
		LegalText:       cfg.EmailLegalText,
	}

	// Each send goes to the email service when the adopter has turned it on in
	// core's settings, otherwise to SMTP. The setting is re-read per send
	// through a short cache, so a change applies without a restart.
	emailService := emailservice.NewProvider(corev1.NewEmailServiceSecretServiceClient(coreConn), kv, 0)
	smtpFallback, err := mail.BuildDefaultSMTPTransport(senderCfg)
	if err != nil {
		return fmt.Errorf("mail: smtp transport: %w", err)
	}
	mailOutbox := mailOutboxStore{store: store.NewMailOutboxStore(db)}
	mailMeter := otel.Meter(serviceName + "/mail")

	// The breaker's onClose runs before the drainer exists at start-up.
	var mailDrainer *mail.Drainer
	breaker := mail.NewBreaker(logger,
		mail.WithBreakerThreshold(cfg.EmailServiceBreakerThreshold),
		mail.WithBreakerCooldown(time.Duration(cfg.EmailServiceBreakerCooldownSec)*time.Second),
		mail.WithBreakerOnClose(func() {
			if mailDrainer != nil {
				mailDrainer.Trigger()
			}
		}),
	)
	routingTransport := mail.NewRoutingTransport(emailService, smtpFallback,
		mail.WithMailgunFactory(func(c emailservice.MailgunConfig) email.Transport {
			return mail.NewBreakerTransport(mail.NewMailgunTransport(mail.MailgunConfig{
				APIKey: c.APIKey, Domain: c.Domain, Region: c.Region, FromAddress: c.FromAddress,
			}), breaker)
		}),
	)
	usageGuard := mail.NewUsageGuard(emailServiceUsageReader{provider: emailService}, mailMeter, logger,
		mail.WithUsageSoftThreshold(cfg.EmailServiceUsageSoftThreshold),
		mail.WithUsageHardCeiling(cfg.EmailServiceUsageHardCeiling),
		mail.WithUsagePollInterval(time.Duration(cfg.EmailServiceUsagePollSec)*time.Second),
	)
	pauseDecider := mail.NewPauseDecider(emailService, breaker, usageGuard, cfg.SMTPHost != "")

	senderOpts := []mail.Option{
		mail.WithTransport(routingTransport),
		mail.WithRecorder(mail.NewAuditRecorder(auditEmitter)),
		mail.WithResolver(prefResolver),
		mail.WithPauseGate(pauseDecider, mailOutbox),
		mail.WithDeduper(notifSentStore),
	}
	// One secret signs the unsubscribe, preferences and email-verification
	// links (the signer namespaces tokens by purpose); without it all three
	// are off.
	var notifySigner *notifytoken.Signer
	if cfg.UnsubscribeSecret != "" {
		notifySigner, err = notifytoken.NewSigner(cfg.UnsubscribeSecret)
		if err != nil {
			return fmt.Errorf("unsubscribe signer: %w", err)
		}
		senderOpts = append(senderOpts, mail.WithUnsubscribeLinks(notifySigner, cfg.UnsubscribeEndpointURL,
			cfg.PortalPreferencesURL, time.Duration(cfg.UnsubscribeLinkTTLHours)*time.Hour))
	} else {
		lg.Info("NOTIFY_UNSUB_SECRET is not set: unsubscribe, preferences and verification links are off")
	}
	mailSender, err := mail.NewSender(senderCfg, senderOpts...)
	if err != nil {
		return fmt.Errorf("mail sender: %w", err)
	}
	mailDrainer = mail.NewDrainer(mailOutbox, mailSender, logger, mail.WithDrainRate(cfg.MailOutboxDrainRatePerSec))
	go mailDrainer.Run(ctx)
	go usageGuard.Run(ctx)

	pushCh := notify.NewPushChannel(cfg.FCMProjectID, identityClient)
	if cfg.FCMProjectID == "" {
		lg.Info("FCM_PROJECT_ID is not set: push is off")
	}
	dispatcher := notify.NewDispatcher(notifPrefStore,
		notify.NewEmailChannel(mailSender, identityClient, cfg.PortalAckQueueURL, cfg.PortalPreferencesURL),
		notify.NewInAppChannel(notifStore), pushCh).
		WithResolver(prefResolver).
		WithOutbox(notifOutboxWriter{store: notifOutboxStore})

	newUserGate := store.NewUserFirstSeenStore(db, time.Duration(cfg.NewUserAckGraceHours)*time.Hour)
	notifiedTracker := store.NewUserVersionNotifiedStore(db)
	welcomeGate := store.NewWelcomeSentStore(db)
	approvalNotified := store.NewApprovalNotifiedStore(db)

	bindings := []binding{
		{"obligations.policy.published", "policy.published", "obligations-policy-published",
			consumer.NewPolicyPublishedConsumer(resolver, ackRowStore, dispatcher, consumer.NewPlatformAckAudienceAuditor(auditEmitter)).
				WithCacheInvalidator(cachedCore).WithNewUserGate(newUserGate).
				WithNewUserThrottleEnabled(cfg.NewUserAckThrottleEnabled).
				WithNotifiedTracker(notifiedTracker).WithPortalURL(cfg.PortalAckQueueURL)},
		{"obligations.policy.obligation_changed", "policy.obligation_changed", "obligations-reconcile",
			consumer.NewObligationChangedConsumer(resolver).WithCacheInvalidator(cachedCore)},
		{"obligations.policy.retired", "policy.retired", "obligations-policy-retired",
			consumer.NewPolicyRetiredConsumer(resolver, dispatcher).WithCacheInvalidator(cachedCore).
				WithNotifiedTracker(store.NewPolicyRetiredNotifiedStore(db))},
		{"obligations.membership.changed", "membership.changed", "obligations-membership",
			consumer.NewMembershipChangedConsumer(resolver)},
		{"obligations.sso.lifecycle", "sso.lifecycle", "obligations-sso-lifecycle",
			consumer.NewSSOLifecycleConsumer(mailSender, identityClient, staticSiteAdminResolver(cfg.SiteAdminEmails)).
				WithWelcomeGate(welcomeGate).WithLoginURL(cfg.PortalAccountURL)},
		{"obligations.account.created", "account.created", "obligations-account-created",
			consumer.NewAccountCreatedConsumer(identityClient, mailSender, welcomeGate, cfg.PortalAccountURL)},
		{"obligations.auth.recovery", "auth.recovery", "obligations-auth-recovery",
			consumer.NewAuthRecoveryConsumer(mailSender, identityClient)},
		{"obligations.workflow.approval_requested", "workflow.approval_requested", "obligations-workflow-approval",
			consumer.NewWorkflowApprovalConsumer(mailSender, identityClient, resolver, approvalNotified, cfg.PortalApprovalsURL, cfg.PortalPreferencesURL)},
		{"obligations.workflow.started", "workflow.started", "obligations-workflow-started",
			consumer.NewWorkflowStartedConsumer(mailSender, identityClient, resolver, approvalNotified, cfg.PortalWorkflowsURL, cfg.PortalPreferencesURL)},
		{"obligations.workflow.denied", "workflow.denied", "obligations-workflow-denied",
			consumer.NewWorkflowDeniedConsumer(mailSender, identityClient, resolver, approvalNotified, cfg.PortalWorkflowsURL, cfg.PortalPreferencesURL)},
	}
	if notifySigner != nil {
		bindings = append(bindings, binding{"obligations.account.created.verify", "account.created", "obligations-email-verification",
			consumer.NewEmailVerificationConsumer(identityClient, mailSender, notifySigner,
				cfg.VerifyEmailEndpointURL, time.Duration(cfg.VerifyEmailLinkTTLHours)*time.Hour)})
	}
	consume(ctx, mqConn, logger, bindings)

	// A Postgres advisory-lock leader runs each tick on exactly one replica.
	if cfg.SchedulerEnabled {
		sweep := scheduler.NewSweep(scheduler.SweepConfig{
			Lister: resolver, State: notifiedTracker, Reminders: dispatcher, Escalator: dispatcher,
			Cadence: prefResolver,
			// Identity has no manager lookup yet, so an escalation is recorded
			// (reminders stop) but no manager is emailed.
			Managers:  noManagerResolver{},
			Backoff:   scheduler.BackoffConfig{EscalationAfter: time.Duration(cfg.EscalationAfterDays) * 24 * time.Hour},
			PortalURL: cfg.PortalAckQueueURL,
		})
		drain := scheduler.NewDrain(scheduler.DrainConfig{
			Outbox: notifOutboxStore, PendingAck: resolver,
			Candidates: pendingAckCandidates{categories: notifCategoryStore, overrides: notifOverrideStore},
			Windows:    notifDigestStore, Cadence: prefResolver, Sender: mailSender, Emails: identityClient,
			Guard: notifSentStore, PreferencesURL: cfg.PortalPreferencesURL, PortalURL: cfg.PortalAckQueueURL,
		})
		go scheduler.NewTicker(time.Duration(cfg.SchedulerTickSec)*time.Second, scheduler.NewLeader(db.Pool()),
			scheduler.Job{Name: "reminder-sweep", Run: sweep.Run},
			scheduler.Job{Name: "digest-drain", Run: drain.DrainOnce},
		).Run(ctx)
		lg.Info("scheduler on", log.F("tick_sec", cfg.SchedulerTickSec), log.F("escalation_after_days", cfg.EscalationAfterDays))
	}

	ackSvc := ackpkg.NewService(ackStoreAdapter, ackAuditEmitter(auditEmitter)).
		WithTx(func(ctx context.Context, fn func(context.Context) error) error { return store.InTx(ctx, db, fn) })
	if len(cfg.TrustedCallers) == 0 {
		lg.Warn("OBLIGATIONS_TRUSTED_CALLERS is not set: forwarded actors are ignored, so acknowledgements are refused")
	}

	checker, err := readiness.New(deps, health.WithTTL(5*time.Second), health.WithTimeout(2*time.Second), health.WithLogger(lg))
	if err != nil {
		return fmt.Errorf("readiness: %w", err)
	}
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", ":"+cfg.GRPCPort)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	probeLis, err := lc.Listen(ctx, "tcp", ":"+cfg.ProbePort)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	lg.Info("serving", log.F("port", cfg.GRPCPort), log.F("probe_port", cfg.ProbePort))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	probesDone := make(chan error, 1)
	go func() {
		probesDone <- server.ServeProbes(ctx, probeLis, checker)
		cancel()
	}()
	opts := server.Options{
		CertFile: cfg.TLS.CertFile, KeyFile: cfg.TLS.KeyFile, ClientCAFile: cfg.TLS.ClientCAFile,
		TrustedCallers: cfg.TrustedCallers, Checker: checker,
	}
	err = server.Serve(ctx, lis, lg, opts, func(s *grpc.Server) {
		obligationsv1.RegisterAckServiceServer(s, grpcsvc.NewAckHandler(ackSvc, policyViewStore))
		obligationsv1.RegisterNotifPrefServiceServer(s, grpcsvc.NewNotifPrefHandler(notifPrefStore).WithSettings(notifSettings))
		obligationsv1.RegisterReportingServiceServer(s, grpcsvc.NewReportingHandler(reporting.NewExporter(store.NewAckExportStore(db)), resolver))
		obligationsv1.RegisterObligationServiceServer(s, grpcsvc.NewObligationHandler(resolver))
		obligationsv1.RegisterWelcomeServiceServer(s, grpcsvc.NewWelcomeHandler(identityClient, mailSender, cfg.PortalAccountURL))
	})
	cancel()
	return errors.Join(err, <-probesDone)
}
