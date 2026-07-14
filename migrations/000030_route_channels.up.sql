-- Route channel 是自定义线路（pool_kind='explicit'）的渠道池成员（阶段 15）。
-- pool_kind='all' 的线路不得有 route_channels 行；mode='fixed' 必须恰好一条（由 service 层强校验）。
CREATE TABLE public.route_channels (
    -- route_id: 所属线路，线路删除时级联清理。--
    route_id bigint NOT NULL,
    -- channel_id: 纳入该线路候选池的 channel ID。--
    channel_id bigint NOT NULL
);

ALTER TABLE ONLY public.route_channels
    ADD CONSTRAINT route_channels_pkey PRIMARY KEY (route_id, channel_id);

CREATE INDEX idx_route_channels_channel ON public.route_channels USING btree (channel_id);

ALTER TABLE ONLY public.route_channels
    ADD CONSTRAINT route_channels_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);

ALTER TABLE ONLY public.route_channels
    ADD CONSTRAINT route_channels_route_id_fkey FOREIGN KEY (route_id) REFERENCES public.routes(id) ON DELETE CASCADE;
