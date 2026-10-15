#!/usr/bin/env python3
"""Bounded HTTPS load and crash rehearsal in a newly created synthetic stack only."""
import argparse
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
import json
import math
import os
from pathlib import Path
import platform
import subprocess
import threading
import time
import uuid

import check_production as rehearsal
import operations
import production as p

LEVELS = (5, 10, 20)
PHASE_INTERVAL = 1.25  # 20-request bursts below the configured 20/sec edge rate.
FORBIDDEN = {'is_correct', 'correct', 'explanation', 'pass_percent', 'points',
             'score_points', 'max_points', 'email', 'phone', 'candidate_id', 'token_hash'}


class CheckFailure(RuntimeError):
    """Contains only a fixed check label/status, never an HTTP path or body."""


def ensure(condition, message):
    if not condition:
        raise CheckFailure(message)


def no_answer_keys(value):
    if isinstance(value, dict):
        ensure(not FORBIDDEN.intersection(value), 'Candidate response exposed a private field.')
        for child in value.values():
            no_answer_keys(child)
    elif isinstance(value, list):
        for child in value:
            no_answer_keys(child)


def percentile(values, fraction):
    ensure(bool(values), 'No latency observations were collected.')
    return sorted(values)[max(0, math.ceil(len(values) * fraction) - 1)]


class Metrics:
    def __init__(self):
        self.lock = threading.Lock()
        self.samples = defaultdict(list)
        self.statuses = defaultdict(Counter)
        self.active = self.peak = 0

    def begin(self):
        with self.lock:
            self.active += 1
            self.peak = max(self.peak, self.active)

    def end(self, label, status, milliseconds):
        with self.lock:
            self.active -= 1
            self.samples[label].append(milliseconds)
            self.statuses[label][str(status)] += 1

    def summary(self):
        return {name: {'requests': len(values), 'statuses': dict(self.statuses[name]),
                       'p50_ms': round(percentile(values, .5), 2),
                       'p95_ms': round(percentile(values, .95), 2),
                       'max_ms': round(max(values), 2)} for name, values in sorted(self.samples.items())}


class Client:
    def __init__(self, config, context, metrics):
        self.config, self.context, self.metrics = config, context, metrics

    def call(self, method, path, body=None, *, label, expected=(200,), json_body=True):
        client = rehearsal.LocalTLS('hr-testing.test', int(self.config['HR_HTTPS_PORT']),
                                    context=self.context, timeout=25)
        started, status = time.perf_counter(), 0
        self.metrics.begin()
        try:
            client.request(method, path, body=json.dumps(body) if body is not None else None,
                           headers={'Origin': self.config['HR_PUBLIC_ORIGIN'], 'Content-Type': 'application/json'})
            response = client.getresponse()
            status = response.status
            payload = response.read()
            ensure(status in expected, label + ': unexpected HTTP ' + str(status))
            if json_body:
                try:
                    payload = json.loads(payload) if payload else None
                except (ValueError, UnicodeDecodeError):
                    raise CheckFailure(label + ': invalid JSON response.') from None
            return status, payload
        except RuntimeError:
            raise
        except Exception:
            # Never include a bearer path, response body or socket exception.
            raise CheckFailure(label + ': transport failed.') from None
        finally:
            client.close()
            self.metrics.end(label, status, (time.perf_counter() - started) * 1000)


class Phases:
    def __init__(self):
        self.last = 0

    def run(self, jobs):
        ensure(1 <= len(jobs) <= 20, 'Rehearsal concurrency must be between 1 and 20.')
        time.sleep(max(0, self.last + PHASE_INTERVAL - time.monotonic()))
        gate = threading.Barrier(len(jobs), timeout=15)

        def work(job):
            gate.wait()
            return job()

        self.last = time.monotonic()
        with ThreadPoolExecutor(max_workers=len(jobs)) as pool:
            return list(pool.map(work, jobs))


def owned_fixture(config):
    state = Path(config['HR_STATE_DIR']).resolve()
    ensure(config['HR_PROJECT'].startswith('hr-prod-test-') and state.name == 'state'
           and state.parent.name.startswith('hr-production-smoke-')
           and state.parent.parent == Path('/tmp').resolve()
           and config['HR_BIND_ADDRESS'] == '127.0.0.1'
           and config['HR_PUBLIC_HOST'] == 'hr-testing.test',
           'Crash rehearsal requires its own temporary synthetic installation.')


def create_bank(hr):
    document = hr('PATCH', '/api/v1/privacy', {
        'base_revision': 0, 'operator_name': 'Synthetic rehearsal only', 'contact': 'operator@rehearsal.test',
        'consent_text': 'Synthetic testing only; no real personal data.',
        'policy_text': 'Temporary isolated rehearsal; all fixtures are destroyed afterwards.', 'approved': True})
    keys, items = {}, []
    for kind, points in (('single_choice', '2.50'), ('verbal_tfu', '1.25'), ('multiple_choice', '0.25')):
        labels = ['A', 'B', 'C'] if kind != 'single_choice' else ['A', 'B']
        if kind == 'verbal_tfu':
            labels = ['Верно', 'Неверно', 'Недостаточно информации']
        question = hr('POST', '/api/v1/questions', {'body': 'Synthetic ' + kind, 'type': kind,
            'passage': 'Synthetic passage.' if kind == 'verbal_tfu' else '', 'difficulty': 2,
            'explanation': 'PRIVATE REHEARSAL EXPLANATION',
            'options': [{'label': label, 'is_correct': n == 0 or (kind == 'multiple_choice' and n == 1)} for n, label in enumerate(labels)]}, expected=201)
        source = hr('GET', '/api/v1/questions/' + question['id'])
        keys[kind] = {'correct': [v['id'] for v in source['options'] if v['is_correct']],
                      'wrong': [v['id'] for v in source['options'] if not v['is_correct']][:1]}
        hr('POST', '/api/v1/questions/' + question['id'] + '/publish', {'base_version_no': 1})
        items.append({'question_version_id': source['version_id'], 'points': points})
    versions = {}
    for show in (True, False):
        test = hr('POST', '/api/v1/tests', {'title': 'Synthetic resilience test', 'duration_seconds': 900,
            'pass_percent': '62.50', 'show_candidate_score': show, 'shuffle_questions': True,
            'shuffle_options': True, 'items': items}, expected=201)
        hr('POST', '/api/v1/tests/' + test['id'] + '/publish', {'base_version_no': 1})
        versions[show] = hr('GET', '/api/v1/tests/' + test['id'])['version_id']
    return document['revision'], keys, versions


def issue(hr, version, index, seconds=3600):
    candidate = hr('POST', '/api/v1/candidates', {'full_name': 'Synthetic rehearsal ' + str(index)}, expected=201)
    invitation = hr('POST', '/api/v1/invitations', {'candidate_id': candidate['id'], 'test_version_id': version,
        'max_attempts': 1, 'expires_at': (datetime.now(timezone.utc) + timedelta(seconds=seconds)).isoformat()}, expected=201)
    return {'prefix': '/api/v1/invite/' + invitation['path'][3:], 'token': invitation['path'][3:],
            'start_body': {'request_id': str(uuid.uuid4()), 'consent': True}, 'index': index}


def attempt_path(case):
    return case['prefix'] + '/attempts/' + case['attempt']['id']


def answer_path(case, item):
    return attempt_path(case) + '/items/' + item['id'] + '/answer'


def answer_body(item, selected):
    return {'request_id': str(uuid.uuid4()), 'expected_revision': item['revision'], 'selected_option_ids': selected}


def item_of(attempt, kind):
    return next(item for item in attempt['items'] if item['type'] == kind)


def check_snapshot(before, after):
    ensure(before['id'] == after['id'] and before['deadline_at'] == after['deadline_at'], 'Attempt/deadline changed after recovery.')
    ensure([(x['id'], x['options'], x['selected_option_ids'], x['revision']) for x in before['items']]
           == [(x['id'], x['options'], x['selected_option_ids'], x['revision']) for x in after['items']],
           'Order or confirmed answers changed after recovery.')


def load_group(size, client, hr, phases, revision, keys, versions):
    cases = [issue(hr, versions[n % 4 < 2], n) for n in range(size)]
    label = 'load_' + str(size)
    for case in cases:
        case['start_body']['consent_revision'] = revision
    landing = phases.run([lambda c=c: client.call('GET', c['prefix'], label=label + '_landing')[1] for c in cases])
    for payload in landing:
        no_answer_keys(payload)
    starts = phases.run([lambda c=c: client.call('POST', c['prefix'] + '/start', c['start_body'], label=label + '_start')[1] for c in cases])
    for case, attempt in zip(cases, starts):
        no_answer_keys(attempt)
        case['attempt'], case['writes'] = attempt, []
        ensure(attempt['status'] == 'in_progress' and 'score' not in attempt, 'A new attempt exposed results.')
    repeated = phases.run([lambda c=c: client.call('POST', c['prefix'] + '/start', c['start_body'], label=label + '_start_retry')[1] for c in cases])
    for before, after in zip(starts, repeated):
        check_snapshot(before, after)
    for kind in ('single_choice', 'verbal_tfu', 'multiple_choice'):
        jobs = []
        for case in cases:
            item = item_of(case['attempt'], kind)
            selected = keys[kind]['wrong' if kind == 'single_choice' and case['index'] % 2 else 'correct']
            body = answer_body(item, selected)
            case['writes'].append((item, body))
            jobs.append(lambda c=case, i=item, b=body: client.call('PUT', answer_path(c, i), b, label=label + '_save')[1])
        saved = phases.run(jobs)
        ensure(all(row['revision'] == 1 for row in saved), 'Concurrent autosave did not commit revision 1.')
        for case, saved_row in zip(cases, saved):
            case['writes'][-1] += (saved_row,)
    retried = phases.run([lambda c=c: client.call('PUT', answer_path(c, c['writes'][0][0]), c['writes'][0][1], label=label + '_save_retry')[1] for c in cases])
    for case, payload in zip(cases, retried):
        ensure(payload == case['writes'][0][2], 'Idempotent save changed its receipt.')
    resumed = phases.run([lambda c=c: client.call('GET', c['prefix'] + '/attempt', label=label + '_resume')[1] for c in cases])
    for case, payload in zip(cases, resumed):
        no_answer_keys(payload)
        ensure(payload['id'] == case['attempt']['id'] and payload['deadline_at'] == case['attempt']['deadline_at'], 'Resume changed attempt/deadline.')
        for item, body, _ in case['writes']:
            current = next(row for row in payload['items'] if row['id'] == item['id'])
            ensure(current['options'] == item['options'] and current['revision'] == 1
                   and sorted(current['selected_option_ids']) == sorted(body['selected_option_ids']), 'Resume lost a confirmed answer.')
    submitted = phases.run([lambda c=c: client.call('POST', attempt_path(c) + '/submit', label=label + '_submit')[1] for c in cases])
    repeated = phases.run([lambda c=c: client.call('POST', attempt_path(c) + '/submit', label=label + '_submit_retry')[1] for c in cases])
    final = phases.run([lambda c=c: client.call('GET', c['prefix'] + '/attempt', label=label + '_finished')[1] for c in cases])
    for case, result, again, restored in zip(cases, submitted, repeated, final):
        expected = '37.50' if case['index'] % 2 else '100.00'
        for row in (result, again, restored):
            no_answer_keys(row)
            ensure(row['status'] == 'submitted' and row['id'] == case['attempt']['id'], 'Submission was not idempotent.')
            if case['index'] % 4 < 2:
                ensure(row.get('score', {}).get('percent') == expected, 'Candidate score is incorrect.')
            else:
                ensure('score' not in row, 'Hidden candidate score was exposed.')
        report = hr('GET', '/api/v1/results/' + result['id'])
        ensure(report['score_percent'] == expected and report['max_points'] == '4.00'
               and report['answered'] == 3 and report['correct'] == (2 if case['index'] % 2 else 3), 'HR report disagrees with saved answers.')
    return cases


def wait_ready(client, limit=20):
    end = time.monotonic() + limit
    while time.monotonic() < end:
        status, _ = client.call('GET', '/api/v1/status', label='recovery_probe', expected=(200, 502, 503, 504), json_body=False)
        if status == 200:
            return
        time.sleep(.2)
    raise RuntimeError('API readiness did not recover within the rehearsal limit.')


def crash_checks(config, client, hr, phases, revision, keys, versions):
    case = issue(hr, versions[True], 'races')
    case['start_body']['consent_revision'] = revision
    starts = phases.run([lambda: client.call('POST', case['prefix'] + '/start', case['start_body'], label='race_start')[1] for _ in range(10)])
    case['attempt'] = starts[0]
    ensure(len({row['id'] for row in starts}) == 1, 'Concurrent starts created duplicate attempts.')
    for row in starts:
        check_snapshot(starts[0], row)
    item = item_of(case['attempt'], 'single_choice')
    first = answer_body(item, keys['single_choice']['correct'])
    receipts = phases.run([lambda: client.call('PUT', answer_path(case, item), first, label='race_same_save')[1] for _ in range(10)])
    ensure(all(row == receipts[0] for row in receipts) and receipts[0]['revision'] == 1, 'Duplicate saves created multiple revisions.')
    alternatives = [dict(first, request_id=str(uuid.uuid4()), expected_revision=1, selected_option_ids=keys['single_choice'][kind]) for kind in ('correct', 'wrong')]
    raced = phases.run([lambda b=b: client.call('PUT', answer_path(case, item), b, label='race_conflict', expected=(200, 409)) for b in alternatives])
    ensure(sorted(status for status, _ in raced) == [200, 409], 'Stale concurrent write was not rejected.')
    checkpoint = client.call('GET', case['prefix'] + '/attempt', label='race_resume')[1]
    winner = next(body for status, body in raced if status == 200)
    ensure(item_of(checkpoint, 'single_choice')['selected_option_ids'] == winner['selected_option_ids'], 'Concurrent write silently replaced the winner.')
    lost_ack = answer_body(item_of(checkpoint, 'single_choice'), keys['single_choice']['correct'])
    client.call('PUT', answer_path(case, item), lost_ack, label='lost_ack_save')  # Application discards this acknowledgement.
    p.compose(config, 'stop', '-t', '0', 'api')
    unsent = dict(lost_ack, request_id=str(uuid.uuid4()), expected_revision=3, selected_option_ids=keys['single_choice']['wrong'])
    client.call('PUT', answer_path(case, item), unsent, label='api_offline_save', expected=(502, 504), json_body=False)
    p.compose(config, 'up', '-d', '--no-deps', '--wait', 'api')
    wait_ready(client)
    recovered = client.call('GET', case['prefix'] + '/attempt', label='api_recovered')[1]
    ensure(recovered['id'] == checkpoint['id'] and recovered['deadline_at'] == checkpoint['deadline_at'], 'API crash reset the attempt/deadline.')
    saved_item = item_of(recovered, 'single_choice')
    ensure(saved_item['revision'] == 3 and saved_item['selected_option_ids'] == keys['single_choice']['correct'], 'API crash lost a committed or accepted an offline write.')
    retry = client.call('PUT', answer_path(case, item), lost_ack, label='lost_ack_retry')[1]
    ensure(retry['revision'] == 3, 'Lost acknowledgement retry saved a duplicate revision.')
    confirmed = client.call('PUT', answer_path(case, item), unsent, label='offline_retry')[1]
    ensure(confirmed['revision'] == 4, 'Unconfirmed write did not recover after retry.')
    for kind in ('verbal_tfu', 'multiple_choice'):
        current = item_of(recovered, kind)
        client.call('PUT', answer_path(case, current), answer_body(current, keys[kind]['correct']), label='crash_checkpoint_save')
    checkpoint = client.call('GET', case['prefix'] + '/attempt', label='crash_checkpoint')[1]
    p.compose(config, 'stop', '-t', '0', 'db')
    _, readiness = client.call('GET', '/api/v1/status', label='db_offline_readiness', expected=(503,))
    ensure(readiness.get('code') == 'database_unavailable', 'Readiness reported a stopped DB as healthy.')
    client.call('GET', '/', label='db_offline_static', json_body=False)
    # Liveness stays successful while DB-dependent readiness fails.
    alive = p.compose(config, 'exec', '-T', 'api', 'wget', '-q', '-O', '-', 'http://127.0.0.1:8080/healthz', capture=True)
    ensure(json.loads(alive.stdout).get('status') == 'ok', 'API liveness unexpectedly depends on DB availability.')
    pending = answer_body(item_of(checkpoint, 'single_choice'), keys['single_choice']['correct'])
    client.call('PUT', answer_path(case, item), pending, label='db_offline_save', expected=(500, 502, 503, 504), json_body=False)
    p.compose(config, 'up', '-d', '--no-deps', '--wait', 'db')
    wait_ready(client)
    recovered = client.call('GET', case['prefix'] + '/attempt', label='db_recovered')[1]
    check_snapshot(checkpoint, recovered)
    committed = client.call('PUT', answer_path(case, item), pending, label='db_retry')[1]
    ensure(committed['revision'] == 5, 'DB recovery retry did not produce exactly one confirmed revision.')
    finished = client.call('POST', attempt_path(case) + '/submit', label='crash_submit')[1]
    ensure(finished.get('score', {}).get('percent') == '100.00', 'Crash recovery changed the final score.')
    hr('GET', '/api/v1/me')  # The original secure session survives both crashes.
    return case


def browserless_expiry(config, client, hr, revision, keys, versions):
    case = issue(hr, versions[True], 'browserless-expiry', seconds=20)
    case['start_body']['consent_revision'] = revision
    case['attempt'] = client.call('POST', case['prefix'] + '/start', case['start_body'], label='expiry_start')[1]
    item = item_of(case['attempt'], 'single_choice')
    body = answer_body(item, keys['single_choice']['correct'])
    receipt = client.call('PUT', answer_path(case, item), body, label='expiry_save')[1]
    p.compose(config, 'stop', '-t', '0', 'api')
    deadline = datetime.fromisoformat(case['attempt']['deadline_at'].replace('Z', '+00:00'))
    remaining = (deadline - datetime.now(timezone.utc)).total_seconds()
    ensure(-1 <= remaining <= 21, 'Unexpected synthetic expiry deadline.')
    time.sleep(max(0, remaining + .3))
    p.compose(config, 'up', '-d', '--no-deps', '--wait', 'api')
    wait_ready(client)
    # Only HR report polling here. Candidate GET would itself expire the attempt.
    for _ in range(40):
        try:
            report = hr('GET', '/api/v1/results/' + case['attempt']['id'])
            break
        except AssertionError:
            time.sleep(.25)
    else:
        raise RuntimeError('Boot worker did not finalize the expired browserless attempt.')
    ensure(report['status'] == 'expired' and report['score_percent'] == '62.50'
           and datetime.fromisoformat(report['finished_at'].replace('Z', '+00:00')) == deadline,
           'Worker expiry did not preserve the deadline and score.')
    client.call('PUT', answer_path(case, item), dict(body, request_id=str(uuid.uuid4()), expected_revision=1),
                label='expiry_new_write', expected=(409,))
    retried = client.call('PUT', answer_path(case, item), body, label='expiry_receipt_retry')[1]
    ensure(retried == receipt, 'Committed receipt was lost after worker expiry.')
    return case


def exercise(config, context, hr, report, p95_limit):
    owned_fixture(config)
    report['stage'] = 'create_synthetic_bank'
    metrics, phases = Metrics(), Phases()
    client = Client(config, context, metrics)
    revision, keys, versions = create_bank(hr)
    cases = []
    for size in LEVELS:
        report['stage'] = 'load_' + str(size)
        start = time.monotonic()
        cases.extend(load_group(size, client, hr, phases, revision, keys, versions))
        report['groups'].append({'candidates': size, 'questions_per_attempt': 3,
                                 'duration_seconds': round(time.monotonic() - start, 2)})
        print(json.dumps({'event': 'load_group_passed', 'candidates': size}), flush=True)
    report['stage'] = 'crash_recovery'
    cases.append(crash_checks(config, client, hr, phases, revision, keys, versions))
    print(json.dumps({'event': 'api_and_db_crash_recovered'}), flush=True)
    report['stage'] = 'browserless_expiry'
    cases.append(browserless_expiry(config, client, hr, revision, keys, versions))
    report['stage'] = 'log_and_performance_checks'
    logs = p.compose(config, 'logs', '--no-color', 'web', 'api', capture=True).stdout
    ensure(all(case['token'] not in logs for case in cases), 'Invitation bearer leaked into application logs.')
    ensure(all(config[key] not in logs for key in p.SECRET_KEYS), 'Configuration secret leaked into application logs.')
    report['metrics'] = metrics.summary()
    report['peak_inflight_requests'] = metrics.peak
    normal = [value for label, values in metrics.samples.items() if label.startswith('load_') for value in values]
    report['normal_requests'] = len(normal)
    report['normal_p95_ms'] = round(percentile(normal, .95), 2)
    report['normal_max_ms'] = round(max(normal), 2)
    report['checks'] = ['mixed_question_scoring', 'hidden_candidate_scores', 'no_answer_keys',
        'idempotent_start_save_submit', 'concurrent_revision_conflict', 'lost_acknowledgement_retry',
        'api_forced_stop_and_resume', 'postgres_forced_stop_and_resume', 'readiness_vs_liveness',
        'browserless_expiry_after_boot', 'late_write_rejected', 'bearer_absent_from_logs']
    ensure(all(status == '200' for label, statuses in metrics.statuses.items() if label.startswith('load_') for status in statuses), 'Healthy load included unsuccessful requests.')
    ensure(report['normal_p95_ms'] <= p95_limit, 'Healthy request p95 exceeded the configured rehearsal threshold.')
    report['exercise_passed'] = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--report', default=str(p.ROOT / '.validation/resilience.json'))
    parser.add_argument('--p95-ms', type=float, default=3000, help='Local rehearsal acceptance threshold, not a production SLA')
    args = parser.parse_args()
    ensure(math.isfinite(args.p95_ms) and args.p95_ms > 0, 'Use a positive finite p95 threshold.')
    path = Path(args.report).expanduser().absolute()
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    ensure(not path.is_symlink(), 'Report must not be a symlink.')
    os.umask(0o077)
    revision = subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=p.ROOT, capture_output=True, text=True, check=True).stdout.strip()
    report = {'version': 1, 'passed': False, 'started_at': datetime.now(timezone.utc).isoformat(),
              'source_commit': revision, 'source_dirty': bool(subprocess.run(['git', 'status', '--porcelain'], cwd=p.ROOT, capture_output=True, text=True, check=True).stdout),
              'environment': 'isolated-local-docker', 'python': platform.python_version(),
              'cpu_count_reported_by_os': os.cpu_count(), 'levels': list(LEVELS), 'groups': [],
              'normal_p95_limit_ms': args.p95_ms}
    report['stage'] = 'prepare_isolated_stack'
    try:
        rehearsal.main(exercise=lambda config, context, hr: exercise(config, context, hr, report, args.p95_ms))
        report['passed'] = True
        print(json.dumps({'event': 'resilience_passed', 'normal_requests': report['normal_requests'],
                          'normal_p95_ms': report['normal_p95_ms'], 'report': str(path)}), flush=True)
        return 0
    except Exception as error:
        report['failure'] = 'Rehearsal did not complete; private HTTP paths and payloads are suppressed.'
        if isinstance(error, CheckFailure):
            report['failure'] = str(error)
        print(json.dumps({'event': 'resilience_failed', 'stage': report['stage'], 'reason': report['failure']}), flush=True)
        return 1
    finally:
        report['finished_at'] = datetime.now(timezone.utc).isoformat()
        operations.atomic_json(path, report)


if __name__ == '__main__':
    raise SystemExit(main())
