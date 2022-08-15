package services

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/RHEnVision/provisioning-backend/internal/clients"
	_ "github.com/RHEnVision/provisioning-backend/internal/clients/image_builder"
	"github.com/RHEnVision/provisioning-backend/internal/clients/sources"
	"github.com/RHEnVision/provisioning-backend/internal/ctxval"
	"github.com/RHEnVision/provisioning-backend/internal/dao"
	"github.com/RHEnVision/provisioning-backend/internal/jobs"
	"github.com/RHEnVision/provisioning-backend/internal/jobs/queue"
	"github.com/RHEnVision/provisioning-backend/internal/models"
	"github.com/RHEnVision/provisioning-backend/internal/payloads"
	"github.com/go-chi/render"
	"github.com/lzap/dejq"
)

func CreateAWSReservation(w http.ResponseWriter, r *http.Request) {
	logger := ctxval.Logger(r.Context())

	var accountId int64 = ctxval.AccountId(r.Context())

	payload := &payloads.AWSReservationRequest{}
	if err := render.Bind(r, payload); err != nil {
		renderError(w, r, payloads.NewInvalidRequestError(r.Context(), err))
		return
	}

	rDao, err := dao.GetReservationDao(r.Context())
	if err != nil {
		renderError(w, r, payloads.NewInitializeDAOError(r.Context(), "reservation DAO", err))
		return
	}
	pkDao, err := dao.GetPubkeyDao(r.Context())
	if err != nil {
		renderError(w, r, payloads.NewInitializeDAOError(r.Context(), "pubkey DAO", err))
		return
	}

	reservation := payload.AWSReservation
	reservation.AccountID = accountId
	reservation.Status = "Created"
	reservation.Provider = models.ProviderTypeAWS

	// validate pubkey
	logger.Debug().Msgf("Validating existence of pubkey %d for this account", reservation.PubkeyID)
	pk, err := pkDao.GetById(r.Context(), reservation.PubkeyID)
	if err != nil {
		var e dao.NoRowsError
		if errors.As(err, &e) {
			renderError(w, r, payloads.NewNotFoundError(r.Context(), err))
		} else {
			renderError(w, r, payloads.NewDAOError(r.Context(), "get pubkey by id", err))
		}
		return
	}
	logger.Debug().Msgf("Found pubkey %d named '%s'", pk.ID, pk.Name)

	// create reservation in the database
	err = rDao.CreateAWS(r.Context(), reservation)
	if err != nil {
		renderError(w, r, payloads.NewDAOError(r.Context(), "create reservation", err))
		return
	}
	logger.Debug().Msgf("Created a new reservation %d", reservation.ID)

	// Get Sources client
	sourcesClient, err := clients.GetSourcesClient(r.Context())
	if err != nil {
		renderError(w, r, payloads.NewClientInitializationError(r.Context(), "sources client v2", err))
		return
	}

	// Fetch arn from Sources
	arn, err := sourcesClient.GetArn(r.Context(), strconv.Itoa(int(payload.SourceID)))
	if err != nil {
		if errors.Is(err, sources.ApplicationNotFoundErr) {
			renderError(w, r, payloads.ClientError(r.Context(), "Sources", "can't fetch arn from sources: application not found", err, 404))
			return
		}
		if errors.Is(err, sources.AuthenticationForSourcesNotFoundErr) {
			renderError(w, r, payloads.ClientError(r.Context(), "Sources", "can't fetch arn from sources: authentication not found", err, 404))
			return
		}
		renderError(w, r, payloads.ClientError(r.Context(), "Sources", "can't fetch arn from sources", err, 500))
		return
	}

	// Prepare jobs
	logger.Debug().Msgf("Enqueuing upload key job for pubkey %d", pk.ID)
	uploadPubkeyJob := dejq.PendingJob{
		Type: queue.TypePubkeyUploadAws,
		Body: &jobs.PubkeyUploadAWSTaskArgs{
			AccountID:     accountId,
			ReservationID: reservation.ID,
			PubkeyID:      pk.ID,
			ARN:           arn,
			SourceID:      reservation.SourceID,
		},
	}

	// Get Image builder client
	IBClient, err := clients.GetImageBuilderClient(r.Context())
	logger.Trace().Msg("Creating IB client")
	if err != nil {
		renderError(w, r, payloads.NewClientInitializationError(r.Context(), "image builder client", err))
		return
	}

	// Get AMI
	ami, err := IBClient.GetAWSAmi(r.Context(), reservation.ImageID)
	if err != nil {
		renderError(w, r, payloads.ClientError(r.Context(), "Image Builder", "can't get ami from image builder", err, 500))
	}

	logger.Debug().Msgf("Enqueuing launch instance job for source %d", reservation.SourceID)
	launchJob := dejq.PendingJob{
		Type: queue.TypeLaunchInstanceAws,
		Body: &jobs.LaunchInstanceAWSTaskArgs{
			AccountID:     accountId,
			ReservationID: reservation.ID,
			PubkeyID:      pk.ID,
			AMI:           ami,
			ARN:           arn,
			Amount:        reservation.Amount,
			InstanceType:  reservation.InstanceType,
		},
	}

	// Enqueue all jobs
	err = queue.GetEnqueuer().Enqueue(r.Context(), uploadPubkeyJob, launchJob)
	if err != nil {
		renderError(w, r, payloads.NewEnqueueTaskError(r.Context(), "AWS reservation", err))
		return
	}

	// Return response payload
	if err := render.Render(w, r, payloads.NewAWSReservationResponse(reservation)); err != nil {
		renderError(w, r, payloads.NewRenderError(r.Context(), "reservation", err))
	}
}