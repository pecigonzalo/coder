import { render, screen } from "@testing-library/react";
import StarterTemplatesPage from "./StarterTemplatesPage";
import { AppProviders } from "App";
import { RouterProvider, createMemoryRouter } from "react-router-dom";
import { RequireAuth } from "contexts/auth/RequireAuth";
import { rest } from "msw";
import {
  MockTemplateExample,
  MockTemplateExample2,
} from "testHelpers/entities";
import { server } from "testHelpers/server";

test("does not display the scratch template", async () => {
  server.use(
    rest.get(
      "api/v2/organizations/:organizationId/templates/examples",
      (req, res, ctx) => {
        return res(
          ctx.status(200),
          ctx.json([
            MockTemplateExample,
            MockTemplateExample2,
            {
              ...MockTemplateExample,
              id: "scratch",
              name: "Scratch",
              description: "Create a template from scratch",
            },
          ]),
        );
      },
    ),
  );

  render(
    <AppProviders>
      <RouterProvider
        router={createMemoryRouter(
          [
            {
              element: <RequireAuth />,
              children: [
                {
                  path: "/starter-templates",
                  element: <StarterTemplatesPage />,
                },
              ],
            },
          ],
          { initialEntries: ["/starter-templates"] },
        )}
      />
    </AppProviders>,
  );

  await screen.findByText(MockTemplateExample.name);
  screen.getByText(MockTemplateExample2.name);
  expect(screen.queryByText("Scratch")).not.toBeInTheDocument();
});