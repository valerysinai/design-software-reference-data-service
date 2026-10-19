// Package hierarchy contains the read use case and its persistence port.
package hierarchy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("institutional hierarchy resource not found")
var ErrConflict = errors.New("institutional hierarchy resource conflicts with an existing resource")

type ListQuery struct {
	Resource       string
	Page, PageSize int
	Sort           string
	Filters        map[string]string
}

type Page struct {
	Data       []json.RawMessage
	TotalItems int
}

type ActiveCatalogReportEntry struct {
	CatalogCode string                `json:"catalog_code"`
	CatalogName string                `json:"catalog_name"`
	Details     []CatalogReportDetail `json:"details"`
}

type CatalogReportDetail struct {
	Code         string `json:"code"`
	Label        string `json:"label"`
	DisplayOrder *int   `json:"display_order"`
}

// Reader is the output port implemented by the PostgreSQL adapter.
type Reader interface {
	List(context.Context, ListQuery) (Page, error)
	Get(context.Context, string, string) (json.RawMessage, error)
	ListCatalogDetails(context.Context, string, ListQuery) (Page, error)
	ReportActiveCatalog(context.Context, string, int, int) ([]ActiveCatalogReportEntry, int, error)
}

// Command preserves field presence so PUT can follow the partial-update API
// contract without confusing an omitted field with an empty value.
type Command struct {
	Name, Code, DaneCode, ParentID, Description, CatalogCode *string
	Key, Value, ValueType                                    *string
	Address, Phone, UnitType                                 *string
	CreatedBy, CorrelationID                                 *string
	IsActive                                                 *bool
	DisplayOrder                                             *int
}

type EventEnvelope struct {
	EnvelopeVersion string          `json:"envelope_version"`
	Version         string          `json:"version"`
	EventID         string          `json:"event_id"`
	EventType       string          `json:"event_type"`
	SourceService   string          `json:"source_service"`
	SourcePort      int             `json:"source_port"`
	Timestamp       string          `json:"timestamp"`
	OccurredAt      string          `json:"occurred_at"`
	CorrelationID   string          `json:"correlation_id,omitempty"`
	Payload         json.RawMessage `json:"payload"`
}

func NewEventID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(bytes[:4]), hex.EncodeToString(bytes[4:6]), hex.EncodeToString(bytes[6:8]), hex.EncodeToString(bytes[8:10]), hex.EncodeToString(bytes[10:]))
}

func NewEventEnvelope(eventType string, payload any, correlationID string, now time.Time) (EventEnvelope, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return EventEnvelope{}, err
	}
	timestamp := now.UTC().Format(time.RFC3339)
	return EventEnvelope{
		EnvelopeVersion: "1.0",
		Version:         "1.0",
		EventID:         NewEventID(),
		EventType:       eventType,
		SourceService:   "reference-data-service",
		SourcePort:      8002,
		Timestamp:       timestamp,
		OccurredAt:      timestamp,
		CorrelationID:   correlationID,
		Payload:         encoded,
	}, nil
}

type ChangeType string

const (
	ChangeTypeAdded       ChangeType = "ADDED"
	ChangeTypeUpdated     ChangeType = "UPDATED"
	ChangeTypeDeactivated ChangeType = "DEACTIVADO"
)

type CatalogChangeItem struct {
	ItemID     string     `json:"item_id"`
	Code       string     `json:"code"`
	Label      string     `json:"label"`
	ChangeType ChangeType `json:"change_type"`
}

type CatalogUpdated struct {
	CatalogID     string              `json:"catalog_id"`
	CatalogCode   string              `json:"catalog_code"`
	ChangedItems  []CatalogChangeItem `json:"changed_items"`
	UpdatedBy     string              `json:"updated_by"`
	UpdatedAt     string              `json:"updated_at"`
	CorrelationID string              `json:"-"`
}

type TrainingCenterCreated struct {
	TrainingCenterID string `json:"training_center_id"`
	CenterCode       string `json:"center_code"`
	Name             string `json:"name"`
	MunicipalityID   string `json:"municipality_id"`
	CreatedBy        string `json:"created_by"`
	CreatedAt        string `json:"created_at"`
	CorrelationID    string `json:"-"`
}

type EventPublisher interface {
	PublishTrainingCenterCreated(context.Context, TrainingCenterCreated) error
	PublishCatalogUpdated(context.Context, CatalogUpdated) error
}

// Writer is the output port for hierarchy administration.
type Writer interface {
	Create(context.Context, string, Command) (json.RawMessage, error)
	Update(context.Context, string, string, Command) (json.RawMessage, error)
	Deactivate(context.Context, string, string, Command, ...string) (json.RawMessage, error)
}

// OutboxWriter persists a business change and its event atomically when the
// storage adapter supports transactional outbox writes.
type OutboxWriter interface {
	CreateWithOutbox(context.Context, string, Command, string, string, func(json.RawMessage) (any, error)) (json.RawMessage, error)
	UpdateWithOutbox(context.Context, string, string, Command, string, string, func(json.RawMessage) (any, error)) (json.RawMessage, error)
	DeactivateWithOutbox(context.Context, string, string, Command, []string, string, string, func(json.RawMessage) (any, error)) (json.RawMessage, error)
}

type QueryService struct{ reader Reader }

func NewQueryService(reader Reader) *QueryService { return &QueryService{reader: reader} }
func (s *QueryService) List(ctx context.Context, query ListQuery) (Page, error) {
	return s.reader.List(ctx, query)
}
func (s *QueryService) Get(ctx context.Context, resource, id string) (json.RawMessage, error) {
	return s.reader.Get(ctx, resource, id)
}
func (s *QueryService) ListCatalogDetails(ctx context.Context, catalogID string, query ListQuery) (Page, error) {
	return s.reader.ListCatalogDetails(ctx, catalogID, query)
}
func (s *QueryService) ReportActiveCatalog(ctx context.Context, catalogCode string, page, pageSize int) ([]ActiveCatalogReportEntry, int, error) {
	return s.reader.ReportActiveCatalog(ctx, catalogCode, page, pageSize)
}

type CommandService struct {
	writer    Writer
	publisher EventPublisher
	useOutbox bool
}

type commandServiceOption func(*CommandService)

func WithEventPublisher(publisher EventPublisher) commandServiceOption {
	return func(service *CommandService) { service.publisher = publisher }
}

func WithTransactionalOutbox() commandServiceOption {
	return func(service *CommandService) { service.useOutbox = true }
}

func NewCommandService(writer Writer, options ...commandServiceOption) *CommandService {
	service := &CommandService{writer: writer}
	for _, option := range options {
		option(service)
	}
	return service
}
func (s *CommandService) Create(ctx context.Context, resource string, command Command) (json.RawMessage, error) {
	if s.publisher != nil && s.useOutbox {
		if writer, ok := s.writer.(OutboxWriter); ok {
			switch resource {
			case "catalogs", "catalog-details":
				return writer.CreateWithOutbox(ctx, resource, command, "catalog", "reference.catalog.updated", func(item json.RawMessage) (any, error) {
					return catalogUpdatedEvent(resource, command, item, ChangeTypeAdded)
				})
			case "training-centers":
				return writer.CreateWithOutbox(ctx, resource, command, "training_center", "reference.training_center.created", func(item json.RawMessage) (any, error) {
					var created struct {
						TrainingCenterID string `json:"id"`
						CenterCode       string `json:"center_code"`
						Name             string `json:"name"`
						MunicipalityID   string `json:"municipality_id"`
						CreatedAt        string `json:"created_at"`
					}
					if err := json.Unmarshal(item, &created); err != nil {
						return nil, err
					}
					createdBy := ""
					if command.CreatedBy != nil {
						createdBy = *command.CreatedBy
					}
					return TrainingCenterCreated{TrainingCenterID: created.TrainingCenterID, CenterCode: created.CenterCode, Name: created.Name, MunicipalityID: created.MunicipalityID, CreatedBy: createdBy, CreatedAt: created.CreatedAt, CorrelationID: correlationID(command)}, nil
				})
			}
		}
	}
	item, err := s.writer.Create(ctx, resource, command)
	if err != nil {
		return item, err
	}
	if s.publisher == nil {
		return item, nil
	}
	switch resource {
	case "catalogs", "catalog-details":
		return item, s.publishCatalogUpdated(ctx, resource, command, item, ChangeTypeAdded)
	case "training-centers":
		return item, s.publishTrainingCenterCreated(ctx, command, item)
	default:
		return item, nil
	}
}

func (s *CommandService) publishTrainingCenterCreated(ctx context.Context, command Command, item json.RawMessage) error {
	var created struct {
		TrainingCenterID string `json:"id"`
		CenterCode       string `json:"center_code"`
		Name             string `json:"name"`
		MunicipalityID   string `json:"municipality_id"`
		CreatedAt        string `json:"created_at"`
	}
	if err := json.Unmarshal(item, &created); err != nil {
		return err
	}
	createdBy := ""
	if command.CreatedBy != nil {
		createdBy = *command.CreatedBy
	}
	return s.publisher.PublishTrainingCenterCreated(ctx, TrainingCenterCreated{
		TrainingCenterID: created.TrainingCenterID,
		CenterCode:       created.CenterCode, Name: created.Name, MunicipalityID: created.MunicipalityID,
		CreatedBy: createdBy, CreatedAt: created.CreatedAt, CorrelationID: correlationID(command),
	})
}
func (s *CommandService) Update(ctx context.Context, resource, id string, command Command) (json.RawMessage, error) {
	if s.publisher != nil && s.useOutbox && (resource == "catalogs" || resource == "catalog-details") {
		if writer, ok := s.writer.(OutboxWriter); ok {
			return writer.UpdateWithOutbox(ctx, resource, id, command, "catalog", "reference.catalog.updated", func(item json.RawMessage) (any, error) {
				return catalogUpdatedEvent(resource, command, item, ChangeTypeUpdated)
			})
		}
	}
	item, err := s.writer.Update(ctx, resource, id, command)
	if err != nil {
		return item, err
	}
	if s.publisher != nil && (resource == "catalogs" || resource == "catalog-details") {
		return item, s.publishCatalogUpdated(ctx, resource, command, item, ChangeTypeUpdated)
	}
	return item, nil
}
func (s *CommandService) Deactivate(ctx context.Context, resource, id string, command Command, parentIDs ...string) error {
	if s.publisher != nil && s.useOutbox && (resource == "catalogs" || resource == "catalog-details") {
		if writer, ok := s.writer.(OutboxWriter); ok {
			_, err := writer.DeactivateWithOutbox(ctx, resource, id, command, parentIDs, "catalog", "reference.catalog.updated", func(item json.RawMessage) (any, error) {
				if resource == "catalog-details" {
					return catalogDeactivatedEvent(command, item, id, parentIDs)
				}
				return catalogUpdatedEvent(resource, command, item, ChangeTypeDeactivated)
			})
			return err
		}
	}
	item, err := s.writer.Deactivate(ctx, resource, id, command, parentIDs...)
	if err != nil {
		return err
	}
	if s.publisher == nil {
		return nil
	}
	if resource == "catalog-details" {
		updated, err := catalogDeactivatedEvent(command, item, id, parentIDs)
		if err != nil {
			return err
		}
		return s.publisher.PublishCatalogUpdated(ctx, updated)
	}
	if resource == "catalogs" {
		return s.publishCatalogUpdated(ctx, resource, command, item, ChangeTypeDeactivated)
	}
	return nil
}

func NewCatalogUpdatedEvent(command Command, item json.RawMessage, changeType ChangeType) (CatalogUpdated, error) {
	return catalogUpdatedEvent("catalog-details", command, item, changeType)
}

func NewCatalogDeactivatedEvent(command Command, item json.RawMessage, id string, parentIDs []string) (CatalogUpdated, error) {
	return catalogDeactivatedEvent(command, item, id, parentIDs)
}

func catalogUpdatedEvent(resource string, command Command, item json.RawMessage, changeType ChangeType) (CatalogUpdated, error) {
	if resource == "catalogs" {
		return catalogEvent(command, item, changeType)
	}
	var detail struct {
		ID        string `json:"id"`
		CatalogID string `json:"catalog_id"`
		Code      string `json:"code"`
		Label     string `json:"label"`
	}
	if err := json.Unmarshal(item, &detail); err != nil {
		return CatalogUpdated{}, err
	}
	catalogID := detail.CatalogID
	if command.ParentID != nil && *command.ParentID != "" {
		catalogID = *command.ParentID
	}
	catalogCode := ""
	if command.CatalogCode != nil {
		catalogCode = *command.CatalogCode
	}
	return CatalogUpdated{
		CatalogID:   catalogID,
		CatalogCode: catalogCode,
		ChangedItems: []CatalogChangeItem{{
			ItemID:     detail.ID,
			Code:       detail.Code,
			Label:      detail.Label,
			ChangeType: changeType,
		}},
		UpdatedBy: func() string {
			if command.CreatedBy != nil {
				return *command.CreatedBy
			}
			return ""
		}(),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		CorrelationID: correlationID(command),
	}, nil
}

func (s *CommandService) publishCatalogUpdated(ctx context.Context, resource string, command Command, item json.RawMessage, changeType ChangeType) error {
	updated, err := catalogUpdatedEvent(resource, command, item, changeType)
	if err != nil {
		return err
	}
	return s.publisher.PublishCatalogUpdated(ctx, updated)
}

func catalogEvent(command Command, item json.RawMessage, changeType ChangeType) (CatalogUpdated, error) {
	var catalog struct {
		ID   string `json:"id"`
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(item, &catalog); err != nil {
		return CatalogUpdated{}, err
	}
	return CatalogUpdated{
		CatalogID:   catalog.ID,
		CatalogCode: catalog.Code,
		ChangedItems: []CatalogChangeItem{{
			ItemID:     catalog.ID,
			Code:       catalog.Code,
			Label:      catalog.Name,
			ChangeType: changeType,
		}},
		UpdatedBy: func() string {
			if command.CreatedBy != nil {
				return *command.CreatedBy
			}
			return ""
		}(),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		CorrelationID: correlationID(command),
	}, nil
}

func catalogDeactivatedEvent(command Command, item json.RawMessage, id string, parentIDs []string) (CatalogUpdated, error) {
	var detail struct {
		ID          string `json:"id"`
		CatalogID   string `json:"catalog_id"`
		CatalogCode string `json:"catalog_code"`
		Code        string `json:"code"`
		Label       string `json:"label"`
	}
	if err := json.Unmarshal(item, &detail); err != nil {
		return CatalogUpdated{}, err
	}
	catalogID := detail.CatalogID
	if catalogID == "" && len(parentIDs) > 0 {
		catalogID = parentIDs[0]
	}
	catalogCode := detail.CatalogCode
	if catalogCode == "" && command.CatalogCode != nil {
		catalogCode = *command.CatalogCode
	}
	itemID := detail.ID
	if itemID == "" {
		itemID = id
	}
	return CatalogUpdated{
		CatalogID:   catalogID,
		CatalogCode: catalogCode,
		ChangedItems: []CatalogChangeItem{{
			ItemID:     itemID,
			Code:       detail.Code,
			Label:      detail.Label,
			ChangeType: ChangeTypeDeactivated,
		}},
		UpdatedBy: func() string {
			if command.CreatedBy != nil {
				return *command.CreatedBy
			}
			return ""
		}(),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		CorrelationID: correlationID(command),
	}, nil
}

func correlationID(command Command) string {
	if command.CorrelationID != nil {
		return *command.CorrelationID
	}
	return ""
}
