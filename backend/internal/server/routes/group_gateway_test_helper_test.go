package routes

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type unmanagedGatewayTestRepository struct {
	service.GroupManagementRepository
}

func (unmanagedGatewayTestRepository) GatewayAdmission(_ context.Context, _, groupID int64, _, _ bool) (service.GatewayAllocation, func(), error) {
	return service.GatewayAllocation{GroupID: groupID}, func() {}, nil
}

func unmanagedGatewayTestHandler() *handler.GroupManagementHandler {
	return handler.NewGroupManagementHandler(service.NewGroupManagementService(unmanagedGatewayTestRepository{}))
}
