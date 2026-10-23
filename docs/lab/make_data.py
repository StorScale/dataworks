"""The lab's sample data (docs/guide.md): Harbor Outfitters, a small retailer.
Deterministic, so the guide's numbers hold: python3 docs/lab/make_data.py
writes docs/lab/data/*.csv.

  customers.csv           60 customers: names, emails, phone numbers (personal data on purpose), region, segment
  orders.csv              300 orders over 2026-07-01..2026-09-30: region, channel, amount, a few refunds, a few test rows
  campaigns.csv           6 marketing campaigns with budgets
  campaign_responses.csv  180 responses: who responded to which campaign, and whether they converted
  reviews.csv             90 product reviews: free text (a few with an email address in it, on purpose), stars, the order
"""
import csv
import os
import random
from datetime import date, timedelta

random.seed(2026)
HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "data")
os.makedirs(HERE, exist_ok=True)

FIRST = ["Ana", "Ben", "Chloe", "Dev", "Elena", "Femi", "Grace", "Hugo", "Iris", "Jon", "Kai", "Lena", "Marco", "Nia", "Omar", "Priya",
         "Quinn", "Rosa", "Sam", "Tomas", "Uma", "Viktor", "Wren", "Ximena", "Yusuf", "Zara"]
LAST = ["Alvarez", "Brooks", "Chen", "Dubois", "Ekwueme", "Fischer", "Garcia", "Hansen", "Ito", "Jensen", "Kowalski", "Larsen", "Moreau",
        "Novak", "Okafor", "Patel", "Quiroga", "Rossi", "Schmidt", "Tanaka", "Usman", "Varga", "Walsh", "Xu", "Yilmaz", "Zimmerman"]
REGIONS = ["EU", "US", "APAC"]
SEGMENTS = ["consumer", "small business", "enterprise"]
CHANNELS = ["web", "store", "partner"]

customers = []
for i in range(1, 61):
    first, last = random.choice(FIRST), random.choice(LAST)
    region = random.choices(REGIONS, weights=[5, 4, 2])[0]
    cc = {"EU": "+44 20 7946 %04d", "US": "+1 415 555 %04d", "APAC": "+61 2 9999 %04d"}[region] % random.randint(1000, 9999)
    customers.append({"customer_id": i, "first_name": first, "last_name": last,
                      "email": f"{first.lower()}.{last.lower()}{i}@example.com", "phone": cc, "region": region,
                      "segment": random.choices(SEGMENTS, weights=[6, 3, 1])[0],
                      "signed_up": (date(2025, 1, 1) + timedelta(days=random.randint(0, 500))).isoformat()})

orders = []
for i in range(1, 301):
    c = random.choice(customers)
    amount = round(random.lognormvariate(4.2, 0.6), 2)
    orders.append({"order_id": i, "customer_id": c["customer_id"], "ordered_on": (date(2026, 7, 1) + timedelta(days=random.randint(0, 91))).isoformat(),
                   "region": c["region"], "channel": random.choices(CHANNELS, weights=[6, 3, 1])[0], "amount": amount,
                   "refunded": "true" if random.random() < 0.05 else "false", "test": "false"})
for i in range(301, 306):  # test rows, as a real system has: to be filtered out
    orders.append({"order_id": i, "customer_id": 1, "ordered_on": "2026-09-30", "region": "EU", "channel": "web", "amount": 9999.0, "refunded": "false", "test": "true"})

campaigns = [{"campaign_id": 1, "name": "Summer sale", "channel": "email", "started_on": "2026-07-01", "budget": 1200},
             {"campaign_id": 2, "name": "Back to school", "channel": "email", "started_on": "2026-08-10", "budget": 800},
             {"campaign_id": 3, "name": "Partner push", "channel": "partner", "started_on": "2026-07-15", "budget": 500},
             {"campaign_id": 4, "name": "Store weekend", "channel": "store", "started_on": "2026-08-01", "budget": 300},
             {"campaign_id": 5, "name": "Enterprise outreach", "channel": "sales", "started_on": "2026-07-20", "budget": 1500},
             {"campaign_id": 6, "name": "September clearance", "channel": "email", "started_on": "2026-09-05", "budget": 600}]

responses = []
for i in range(1, 181):
    camp, c = random.choice(campaigns), random.choice(customers)
    day = date.fromisoformat(camp["started_on"]) + timedelta(days=random.randint(0, 20))
    responses.append({"response_id": i, "campaign_id": camp["campaign_id"], "customer_id": c["customer_id"], "responded_on": day.isoformat(),
                      "converted": "true" if random.random() < {"email": 0.25, "partner": 0.4, "store": 0.5, "sales": 0.3}[camp["channel"]] else "false"})

# Reviews: what customers say about what they bought, in their own words
# (the lab's "Ask a model" step sorts them into topics). Drawn after the
# rest, so the files above come out the same as before them.
TOPICS = {
    "delivery": ["Took {d} days to arrive, the box was dented but the {p} was fine.", "Delivery was late by {d} days and nobody told me.",
                 "Arrived two days early, well packed. Happy with the {p}."],
    "sizing": ["The {p} runs small, I had to send it back for a larger size.", "Sizing chart is off: ordered medium, fits like a small.",
               "Perfect fit, the size guide for the {p} was spot on."],
    "quality": ["The {p} seam split after {d} days of use. Disappointing.", "Solid build, the {p} survived a week of rain.",
                "Zip on the {p} broke the second time out."],
    "price": ["Good {p} but {amount} is steep for what it is.", "Worth every cent at {amount}.", "Found the same {p} cheaper elsewhere after buying."],
    "service": ["Support answered in an hour and sorted the exchange of my {p}.", "Waited {d} days for a reply about my {p}, then got a form letter.",
                "The store staff helped me pick the right {p}, great service."],
}
PRODUCTS = ["jacket", "tent", "boots", "backpack", "sleeping bag", "headlamp"]
reviews = []
real_orders = [o for o in orders if o["test"] == "false"]
for i in range(1, 91):
    o = random.choice(real_orders)
    topic = random.choice(list(TOPICS))
    text = random.choice(TOPICS[topic]).format(d=random.randint(2, 14), p=random.choice(PRODUCTS), amount=f"${o['amount']:.0f}")
    stars = {"delivery": 3, "sizing": 3, "quality": 2, "price": 3, "service": 4}[topic] + random.choice([-1, 0, 1])
    if i in (7, 31, 58):  # three reviewers sign off with an address: what the gateway's guardrail refuses, and a prepare step masks
        c = next(c for c in customers if c["customer_id"] == o["customer_id"])
        text += f" Write back at {c['email']}."
    reviews.append({"review_id": i, "order_id": o["order_id"], "customer_id": o["customer_id"], "reviewed_on": o["ordered_on"],
                    "stars": max(1, min(5, stars)), "text": text})

for name, rows in [("customers", customers), ("orders", orders), ("campaigns", campaigns), ("campaign_responses", responses), ("reviews", reviews)]:
    with open(os.path.join(HERE, name + ".csv"), "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=list(rows[0].keys()))
        w.writeheader()
        w.writerows(rows)
    print(f"{name}.csv: {len(rows)} rows")

# Two of the company's pages, for Lab 18's knowledge bank of files: the care guide and the returns page.
PAGES = {
    "care.md": """# Caring for your Harbor Outfitters gear

## Jackets
Wash your jacket cold, on a gentle cycle, with a technical wash rather than ordinary detergent. Close every zip first.
Tumble dry low for twenty minutes: the warmth renews the water-repellent finish. Never dry-clean, never iron the shell.

## Tents
Pitch your tent at home once before the trip. After every trip, dry it fully before you pack it away, or it will mildew.
Clean the zips with a soft brush; a stiff zip runs freely again with a little zip lubricant. Never machine-wash a tent.

## Boots
Brush off dried mud, wash with lukewarm water, and let them dry away from a fire or radiator: heat cracks the leather.
Re-proof the leather twice a season. Replace the laces when they fray.

## Sleeping bags and backpacks
Store a sleeping bag loose in its storage sack, never compressed. Wash a backpack by hand in lukewarm water, and empty the pockets first.
""",
    "returns.html": """<!doctype html>
<html><head><meta charset="utf-8"><title>Returns and repairs | Harbor Outfitters</title></head>
<body>
<h1>Returns and repairs</h1>
<p>You may return anything unused within 60 days of delivery for a full refund. Start a return from your order page; the label is free.</p>
<h2>Faulty gear</h2>
<p>If a zip breaks, a seam opens or a buckle snaps within two years, we repair or replace it free of charge. Send us a photo of the fault with your order number.</p>
<h2>Sizing</h2>
<p>Boots and jackets that don't fit may be exchanged for another size within 60 days, worn or not, as long as they're clean.</p>
<h2>Refunds</h2>
<p>Refunds reach your card within five working days of the return arriving at our warehouse.</p>
</body></html>
""",
}
for name, text in PAGES.items():
    with open(os.path.join(HERE, name), "w") as f:
        f.write(text)
    print(f"{name}: {len(text)} characters")
