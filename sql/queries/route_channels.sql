-- name: ListRouteChannels :many
-- ListRouteChannels 列出某线路的渠道池成员 channel_id。
SELECT channel_id FROM route_channels WHERE route_id = sqlc.arg(route_id) ORDER BY channel_id;

-- name: ListRouteChannelsDetailed :many
-- ListRouteChannelsDetailed 列出某线路渠道池，连带渠道展示名/provider，供 admin 管理台展示。
SELECT
    rc.channel_id,
    c.name AS channel_name,
    c.provider_id,
    p.slug AS provider_slug
FROM route_channels rc
JOIN channels c ON c.id = rc.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE rc.route_id = sqlc.arg(route_id)
ORDER BY rc.channel_id;

-- name: CountRouteChannels :one
-- CountRouteChannels 统计某线路渠道池成员数，供 fixed（恰好一条）/explicit（至少一条）校验。
SELECT COUNT(*) FROM route_channels WHERE route_id = sqlc.arg(route_id);

-- name: AddRouteChannel :exec
-- AddRouteChannel 把一条渠道加入线路池；重复加入由主键幂等忽略。
INSERT INTO route_channels (route_id, channel_id)
VALUES (sqlc.arg(route_id), sqlc.arg(channel_id))
ON CONFLICT (route_id, channel_id) DO NOTHING;

-- name: DeleteRouteChannels :exec
-- DeleteRouteChannels 清空某线路的渠道池（设置渠道池前先清空，整体在事务内重建）。
DELETE FROM route_channels WHERE route_id = sqlc.arg(route_id);
