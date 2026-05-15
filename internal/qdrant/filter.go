package qdrant

import (
	"context"

	pb "github.com/qdrant/go-client/qdrant"
)

// ScrollFiltered returns points from the given collection matching the filter,
// ordered by point ID. limit caps results; pass 0 for the Qdrant default (10).
func (c *Client) ScrollFiltered(ctx context.Context, collection string, filter *pb.Filter, limit uint32) ([]*pb.RetrievedPoint, error) {
	req := &pb.ScrollPoints{
		CollectionName: collection,
		WithPayload:    pb.NewWithPayload(true),
	}
	if limit > 0 {
		req.Limit = &limit
	}
	if filter != nil {
		req.Filter = filter
	}

	resp, err := c.points.Scroll(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// DeleteFiltered removes points from the given collection matching the filter.
func (c *Client) DeleteFiltered(ctx context.Context, collection string, filter *pb.Filter) error {
	_, err := c.points.Delete(ctx, &pb.DeletePoints{
		CollectionName: collection,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Filter{
				Filter: filter,
			},
		},
	})
	return err
}
