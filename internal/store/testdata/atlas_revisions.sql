-- The Atlas operator's ledger, as `atlas migrate apply` (v0.36.0, the engine the operator runs)
-- creates it as rooms_owner over v0.45.0's fourteen migration files: cnref's SQLInstance pins
-- atlasSchema at v0.45.0. Dumped with pg_dump --inserts; each hash is the file's atlas.sum line.
-- TestAdopt* load it over the same fourteen files.
CREATE SCHEMA atlas_schema_revisions;
CREATE TABLE atlas_schema_revisions.atlas_schema_revisions (
    version character varying NOT NULL,
    description character varying NOT NULL,
    type bigint DEFAULT 2 NOT NULL,
    applied bigint DEFAULT 0 NOT NULL,
    total bigint DEFAULT 0 NOT NULL,
    executed_at timestamp with time zone NOT NULL,
    execution_time bigint NOT NULL,
    error text,
    error_stmt text,
    hash character varying NOT NULL,
    partial_hashes jsonb,
    operator_version character varying NOT NULL
);
ALTER TABLE ONLY atlas_schema_revisions.atlas_schema_revisions
    ADD CONSTRAINT atlas_schema_revisions_pkey PRIMARY KEY (version);
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20260927120000', 'rooms', 2, 30, 30, '2026-11-13 09:18:02.840356+00', 218195, '', '', '2iHh9ofAjF9QEjy2L4Nk4qPz6GPfyTyceOP/Lg2nitE=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20260929120000', 'verdicts', 2, 1, 1, '2026-11-13 09:18:02.863452+00', 510832, '', '', 'aat8qDZyfEMLP7P/FyvK90fwdXTcVbUIJte49wiEWNg=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261001120000', 'driver_queue', 2, 17, 17, '2026-11-13 09:18:02.868628+00', 315406, '', '', 'nT+Seb7xn/lYr9A+WtDkWtuGeuR/5Qw7YvrF5ueKQ+k=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261001130000', 'delivery_indexes', 2, 2, 2, '2026-11-13 09:18:02.880835+00', 680857, '', '', 'MtIHMwMJb0LCP8wNKBI0J7fGEt5TMuWus9AtgVtpvv8=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261001140000', 'brief_index', 2, 1, 1, '2026-11-13 09:18:02.888685+00', 460711, '', '', 'urCOO2Ske+cvKP/YUYxwUFJDB/gsez1QSkzigqO1NQs=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261005120000', 'approvals', 2, 13, 13, '2026-11-13 09:18:02.89375+00', 189145, '', '', 'OT0qI8o41v1qF+3qUoyBBy7WY92uJWZ1nG1PxXNdvvE=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261005130000', 'approval_indexes', 2, 4, 4, '2026-11-13 09:18:02.908686+00', 659183, '', '', 'Zstpv5r4/Wv1sCBrmuKJsQapiWf9604PKvu7v+Oocs4=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261008120000', 'task_facts_index', 2, 1, 1, '2026-11-13 09:18:02.920287+00', 532166, '', '', 'EXFLZRZjBSVO++W/QACG654INMqlZssYB+tDXfSciDw=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261023120000', 'webhooks', 2, 11, 11, '2026-11-13 09:18:02.926394+00', 245553, '', '', 'tMkdkVLBOTL4yN+I2tkeAgNyy0Vse903Oze84isKo2U=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261028120000', 'slack_links', 2, 9, 9, '2026-11-13 09:18:02.94103+00', 163534, '', '', 'TcZOaZGCt6RNIEs7tCVar7VqzfgwjlkVbYQOY0OAq6M=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261105120000', 'approval_binding', 2, 2, 2, '2026-11-13 09:18:02.949811+00', 191725, '', '', 'N9uBHyWqXvSn/gYc10pVdRU1W3wQ6bhTaQUHJGw8lUU=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261108120000', 'approval_grants', 2, 19, 19, '2026-11-13 09:18:02.953913+00', 167003, '', '', '3wkkj/xhkNsuifng5Gj/xlaDIS21aJFyzqnGVI716yo=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261108130000', 'policy_promotions', 2, 16, 16, '2026-11-13 09:18:02.971351+00', 150814, '', '', 'bKdMFa2xaWMHWqjtZ3dKzYdPYoBBaT/0RPMC+hD8Yrs=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('20261112120000', 'intent_samples', 2, 13, 13, '2026-11-13 09:18:02.985112+00', 182653, '', '', 'zDheQMEGZaAQ3tizW79pJrpbtR3k1d29v8ik8Llsm/Q=', 'null', 'Atlas CLI v0.36.0'); -- pragma: allowlist secret
