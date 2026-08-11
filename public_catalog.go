package main

func bookingActivities() []string {
	return []string{"training", "full_indoor_cricket", "futsal", "badminton", "table_tennis", "cricket_net", "tennis"}
}

func sportsCatalog() []SportPage {
	return []SportPage{
		{
			Slug:             "cricket",
			Name:             "Cricket Nets",
			Kicker:           "Indoor Cricket",
			Summary:          "Train with dedicated cricket net sessions at Mekmaa in Jaffna.",
			ShortDescription: "Practice lanes, repeatable drills and indoor focus for batting and bowling sessions.",
			Detail:           "Mekmaa gives players a dependable indoor cricket environment for technical repetition, small-group practice and structured improvement sessions.",
			Accent:           "bg-amber",
			PrimaryCTA:       "/book",
			PrimaryLabel:     "Book Cricket",
			Highlights:       []string{"Net-based repetition", "Indoor weather-proof practice", "Suitable for individual and small-group sessions"},
		},
		{
			Slug:             "futsal",
			Name:             "Futsal",
			Kicker:           "Indoor Team Play",
			Summary:          "Reserve indoor futsal sessions for teams and fast-paced match play at Mekmaa.",
			ShortDescription: "Clean indoor conditions for training games, competitive sessions and energetic group play.",
			Detail:           "The Mekmaa futsal setup is designed for teams that want consistent indoor conditions, easier planning and a strong environment for recreational or competitive sessions.",
			Accent:           "bg-emerald-500",
			PrimaryCTA:       "/book",
			PrimaryLabel:     "Book Futsal",
			Highlights:       []string{"Team-friendly sessions", "Fast indoor play", "Ideal for regular weekly bookings"},
		},
		{
			Slug:             "badminton",
			Name:             "Badminton",
			Kicker:           "Indoor Court Sessions",
			Summary:          "Play badminton in a comfortable indoor environment at Mekmaa.",
			ShortDescription: "Flexible bookings for casual rallies, match preparation and routine skill work.",
			Detail:           "Badminton sessions at Mekmaa are suited to players who want dependable indoor court time, whether that means social games, coaching support or repeated technical practice.",
			Accent:           "bg-aqua",
			PrimaryCTA:       "/book",
			PrimaryLabel:     "Book Badminton",
			Highlights:       []string{"Indoor comfort", "Casual and competitive use", "Strong option for repeated practice"},
		},
		{
			Slug:             "table-tennis",
			Name:             "Table Tennis",
			Kicker:           "Reflex and Focus",
			Summary:          "Book table tennis sessions at Mekmaa for fast, focused indoor play.",
			ShortDescription: "Indoor tables for quick games, reflex work and flexible training blocks.",
			Detail:           "Mekmaa supports table tennis sessions that reward concentration, timing and repetition, with an easy path for casual games or more focused improvement work.",
			Accent:           "bg-blush",
			PrimaryCTA:       "/book",
			PrimaryLabel:     "Book Table Tennis",
			Highlights:       []string{"Flexible session formats", "Good for individuals and pairs", "Strong indoor setup for focus-based training"},
		},
		{
			Slug:             "tennis",
			Name:             "Tennis",
			Kicker:           "Tennis at Mekmaa",
			Summary:          "Explore tennis opportunities through Mekmaa's indoor sports offering in Jaffna.",
			ShortDescription: "A tennis pathway for players who want structured sport access and want to enquire directly.",
			Detail:           "Tennis is now part of the public sports catalogue at Mekmaa. For current session formats, availability and coaching-related enquiries, players can contact the team directly.",
			Accent:           "bg-lime-200",
			PrimaryCTA:       "/contact?subject=Tennis%20Enquiry",
			PrimaryLabel:     "Enquire About Tennis",
			Highlights:       []string{"Included in the sports catalogue", "Direct enquiry path for availability", "Suitable for players seeking structured access"},
		},
	}
}

func sportBySlug(slug string) (SportPage, bool) {
	for _, sport := range sportsCatalog() {
		if sport.Slug == slug {
			return sport, true
		}
	}
	return SportPage{}, false
}

func sportTemplateNameBySlug(slug string) (string, bool) {
	switch slug {
	case "cricket":
		return "sports-cricket", true
	case "futsal":
		return "sports-futsal", true
	case "badminton":
		return "sports-badminton", true
	case "table-tennis":
		return "sports-table-tennis", true
	case "tennis":
		return "sports-tennis", true
	default:
		return "", false
	}
}

func homeFAQItems() []FAQItem {
	return []FAQItem{
		{Question: "How do I book a session?", Answer: "Use the booking page to review available slots and choose the activity that fits your session. If you need help with a special request, contact the team directly."},
		{Question: "Which sports are available at Mekmaa?", Answer: "Mekmaa currently features cricket nets, futsal, badminton, table tennis and tennis as part of its public sports offering."},
		{Question: "Is coaching available for children and teenagers?", Answer: "Yes. Mekmaa Cricket Academy provides structured coaching with a strong focus on skill development, discipline and confidence for kids and teens."},
		{Question: "Can adults also use the facility?", Answer: "Yes. The facility is positioned as suitable for kids, teens and adults across general bookings and sport sessions."},
		{Question: "How do I enquire about tennis?", Answer: "Tennis is available inside the sports section. Use the tennis sport page or the contact page to ask about session formats and availability."},
	}
}
