ALTER TABLE attempts ADD COLUMN start_request_id uuid;
CREATE UNIQUE INDEX ux_attempt_start_request ON attempts(organization_id,invitation_id,start_request_id);
CREATE UNIQUE INDEX ux_attempt_active ON attempts(organization_id,invitation_id) WHERE status='in_progress';
CREATE INDEX ix_attempt_due ON attempts(deadline_at) WHERE status='in_progress';

-- Durable answer receipts let a retry recover an acknowledgement after a lost response.
CREATE TABLE answer_receipts (
 organization_id uuid NOT NULL,
 attempt_item_id uuid NOT NULL,
 request_id uuid NOT NULL,
 expected_revision integer NOT NULL CHECK(expected_revision>=0),
 selected_option_ids uuid[] NOT NULL,
 revision integer NOT NULL CHECK(revision>=1),
 saved_at timestamptz NOT NULL,
 PRIMARY KEY(organization_id,attempt_item_id,request_id),
 FOREIGN KEY(organization_id,attempt_item_id) REFERENCES attempt_items(organization_id,id)
);
