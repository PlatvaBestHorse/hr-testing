-- Every acknowledged start, including a resume, must retain its request binding.
-- A retry of a resume must never become a fresh attempt after the original finishes.
CREATE TABLE start_receipts (
 organization_id uuid NOT NULL,
 invitation_id uuid NOT NULL,
 request_id uuid NOT NULL,
 attempt_id uuid NOT NULL,
 PRIMARY KEY(organization_id,invitation_id,request_id),
 FOREIGN KEY(organization_id,invitation_id) REFERENCES invitations(organization_id,id),
 FOREIGN KEY(organization_id,attempt_id) REFERENCES attempts(organization_id,id)
);
INSERT INTO start_receipts(organization_id,invitation_id,request_id,attempt_id)
 SELECT organization_id,invitation_id,start_request_id,id FROM attempts WHERE start_request_id IS NOT NULL;
