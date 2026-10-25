# Field service dispatch: requirements

## Background

Hartley Mechanical runs 240 field technicians across 14 depots in three countries. Work arrives from
four places: a customer phone line, a web form, a scheduled maintenance calendar, and alarms raised
automatically by the equipment the company has installed on customer sites. Today a dispatcher reads all
four, decides who goes where, and types the assignment into a spreadsheet that is printed every morning.

The spreadsheet is the only record of who is doing what. When a job overruns, nothing updates. When a
technician calls in sick at 06:30, a dispatcher re-plans forty jobs by hand and phones each customer.
Last year the company measured 18% of visits as "wasted": the technician arrived without the part, the
customer was not there, or two technicians were sent to the same site.

We are replacing the spreadsheet with a dispatch system. This document states what it must do. It does
not state how to build it.

## Who uses it

- **Dispatcher.** Sits in a depot. Owns the day's plan for that depot. Decides exceptions.
- **Technician.** Works from a van. Sees today's jobs on a phone. Reports what happened.
- **Service manager.** Owns a region. Looks at yesterday and last month, not at today.
- **Customer.** Receives an appointment, wants to know when somebody will arrive.
- **Parts coordinator.** Keeps the depot stock and the van stock matching the work that is planned.

## Goals

1. A dispatcher plans a depot day in under 20 minutes, down from the 90 minutes measured today.
2. Wasted visits fall from 18% to under 6% within two quarters of going live.
3. A customer is told a two-hour arrival window, and that window holds 90% of the time.
4. A technician never phones the depot to ask what is next.
5. A service manager can say, without asking anyone, why a job was late.

## Non-goals

- Invoicing and payment collection. The finance system keeps those.
- Managing the customer contract, its renewal or its pricing.
- Routing a technician turn by turn once they are driving. The phone's own maps application does that.
- Tracking the technician's location continuously through the day.

---

# Epic 1: Take in work from every source

Work must arrive in one queue regardless of where it came from, and nothing may be lost in between.

## Requirements

- Every piece of work becomes a job with a single identifier, whatever source raised it.
- A job raised by a customer phone call is created by the dispatcher in under 60 seconds of talking.
- A job raised by the web form reaches the queue without a dispatcher touching it.
- A job raised by the maintenance calendar appears 10 working days before it is due.
- A job raised by an equipment alarm carries the alarm's own reference and the equipment it came from.
- A job that arrives twice from two sources is shown as one job with both sources named.
- A source that stops sending work raises an alert to the service manager within one hour.
- A job records which source raised it for as long as the job exists.

## Acceptance criteria

- A dispatcher can list every job raised today and see its source for each.
- Switching off the web form feed for 70 minutes raises exactly one alert.
- The same equipment alarm sent three times produces one job, not three.
- No job in the queue has an empty source.

---

# Epic 2: Decide who goes where

This is the dispatcher's working surface and the heart of the product.

## Requirements

- A dispatcher sees the day as a row per technician and a column per hour.
- A job that is not yet assigned sits in a visible list beside the plan, never hidden behind a menu.
- Assigning a job to a technician takes one action, and the plan updates immediately.
- The system proposes a technician for each unassigned job, and says in one sentence why.
- A proposal accounts for the skill the job needs, the parts the job needs, where the technician already
  is, and how much of their shift is left.
- A dispatcher can reject a proposal without giving a reason, and the system must not propose the same
  technician for that job again that day.
- A dispatcher can assign a job against the proposal, and the system records that this happened.
- Two technicians can never be assigned to the same job, and the system refuses the second assignment
  rather than warning about it.
- A job that no technician can take by the end of the day is escalated to the service manager at 15:00.
- A dispatcher can see, for any technician, how much of their shift is already committed.

## Acceptance criteria

- A depot with 20 technicians and 60 jobs is planned by one dispatcher in under 20 minutes.
- Every proposal shows its one-sentence reason.
- Attempting to double-assign a job fails and explains why.
- Rejecting a proposal and reloading the page does not bring the same proposal back.

---

# Epic 3: Replan when the day breaks

The plan is wrong within an hour of being made. Replanning is the normal case, not the exception.

## Requirements

- A technician reporting sick frees every one of their jobs back into the unassigned list at once.
- The system proposes a new plan for the freed jobs and shows what it would change before changing it.
- A dispatcher accepts or rejects the proposed replan as a whole, or job by job.
- A job that overruns by more than 30 minutes marks every later job of that technician as at risk.
- A job marked at risk tells the dispatcher which customer is affected and by roughly how long.
- The system never moves a job that a technician has already started.
- A replan that would break a customer's promised window says so before it is accepted.
- Every replan records who accepted it and when.

## Acceptance criteria

- Marking a technician sick at 06:30 with 8 jobs produces a proposed replan within 10 seconds.
- A job running 45 minutes late flags the following jobs, and only that technician's jobs.
- The audit shows the dispatcher's name against every accepted replan.

---

# Epic 4: The technician's day

The technician works from a van, often with one bar of signal and often with gloves on.

## Requirements

- A technician opens the application and sees today's jobs in the order they are to be done.
- Each job shows the customer, the site, the equipment, what was reported, and what parts are expected.
- A technician sees the previous three visits to the same equipment, with what was done each time.
- A technician marks a job as travelling, on site, or finished, with one action each.
- A technician records what they did, which parts they used, and whether the job is complete.
- A technician can record all of this with no signal, and it reaches the depot when signal returns.
- A technician who cannot complete a job gives a reason from a fixed list and may add a sentence.
- A technician is never shown a job that has been taken away from them while they were offline, except
  as a message explaining it was reassigned.
- The application must be usable one-handed and readable in direct sunlight.

## Acceptance criteria

- A full job can be recorded start to finish in aeroplane mode and arrives intact afterwards.
- Reassigning a job while the technician is offline produces an explanatory message, not a silent change.
- A technician sees the three previous visits within two seconds of opening a job.

---

# Epic 5: Tell the customer

## Requirements

- A customer is told a two-hour arrival window as soon as the job is assigned.
- A customer is told again on the morning of the visit.
- A customer whose window moves by more than 30 minutes is told within 5 minutes of the change.
- A customer is told when the technician is on the way, and the message names the technician.
- A customer can confirm or ask to change the appointment without phoning the depot.
- A customer who asks to change the appointment releases the job back to the unassigned list.
- Messages go by text message, by email, or both, according to what the customer chose.
- A customer who has chosen to receive nothing receives nothing, including the on-the-way message.

## Acceptance criteria

- A window moving at 09:00 produces a customer message by 09:05.
- A customer opting out stops every message within one working day.
- The on-the-way message names the technician who is actually travelling.

---

# Epic 6: Parts

A visit without the right part is the most expensive thing the company does.

## Requirements

- A job states the parts it is expected to need, from what was reported and the equipment's history.
- The system does not assign a job to a technician whose van lacks the expected parts, unless the
  dispatcher overrides it.
- A dispatcher overriding a parts check records why in one sentence.
- A parts coordinator sees, for tomorrow's plan, every part that is needed and not in the right van.
- A part used on a job is taken off that van's stock when the technician records it.
- A van whose stock of any part falls below its agreed minimum raises a restock task.
- A part that is needed and not held anywhere in the depot raises a purchase request to the coordinator.

## Acceptance criteria

- Tomorrow's shortfall list is correct against the van stock records at 17:00 today.
- Recording a used part reduces van stock exactly once, including when recorded offline.
- An override without a reason cannot be saved.

---

# Epic 7: Look back

## Requirements

- A service manager sees, per depot and per month: jobs completed, jobs wasted, and visits per job.
- A wasted visit is one that ended without the work being done, and its reason is always recorded.
- A service manager can see why any single job was late, showing the plan as it stood through the day.
- A service manager can compare two depots over the same period.
- The figures a service manager sees are never more than one hour behind.
- Nothing in this area lets anyone change a job.

## Acceptance criteria

- The wasted-visit figure can be reconciled by hand against the underlying jobs for one depot-month.
- Opening a late job shows every replan that affected it, in order.
- The reporting area offers no action that writes.

---

# Constraints

- The company operates in the United Kingdom, Ireland and the Netherlands. Dates, addresses and phone
  numbers follow local conventions, and the interface is English only at launch.
- Technician phones are a mix of three-year-old Android devices and current iPhones.
- Depot connectivity is reliable; van connectivity is not.
- The finance system is the record for anything billable, and it receives a completed job within an hour.
- Customer contact details come from the existing customer system and are never edited here.
- A technician's working hours come from the existing rota system, which is authoritative.
- The company has an existing identity provider and expects one sign-in for every member of staff.

## Volumes

- 240 technicians, 14 depots, about 1,100 jobs on a normal weekday, 1,800 at the winter peak.
- Around 60,000 pieces of customer equipment under contract.
- A depot plans 40 to 110 jobs a day.
- Peak dispatcher use is 07:00 to 09:00 local time, in three time zones.

## What we have measured

- A dispatcher currently spends 90 minutes planning, measured across six depots over two weeks.
- 18% of visits were wasted last year: 7% wrong or missing part, 6% customer absent, 3% duplicate
  dispatch, 2% insufficient time.
- The average job takes 74 minutes on site; the spread is wide, from 15 minutes to a full day.
- 31% of jobs are moved at least once after the plan is made.

## Open questions

- Should a technician be able to accept a job directly from the unassigned list, or must a dispatcher
  always assign it? The depots disagree, and the two larger ones want self-assignment.
- When the proposal engine and the dispatcher disagree repeatedly, who is told?
- How long must a completed job stay readable to a technician? Finance says seven years; the technicians
  say a fortnight is plenty.

## Technical notes from the current team

These are observations about the existing estate, not requirements.

The customer system exposes a read-only interface over HTTP and is rate limited to 10 requests a second;
it has been known to take 4 seconds to answer. The rota system publishes a nightly export rather than
answering questions live. The equipment alarms arrive as email to a shared mailbox today, which is how
duplicates happen. The existing spreadsheet is kept on a shared drive and has no history.
